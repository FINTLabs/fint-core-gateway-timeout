# fint-core-gateway-timeout

A small HTTP server for finding out how long the network path in front of FINT (ingress, load balancer and anything else in between) lets a request wait for its response.

The caller picks how long the server waits, in the URL of each request. Nothing needs to change on the server between runs, and probes with different wait times can run side by side.

## Endpoints

| Endpoint | What the server does | What it tells you |
|---|---|---|
| `GET /hold/{duration}` | Sends nothing at all for `duration`, then answers 200 with a small JSON body. | How long the backend can stay silent before something gives up. |
| `GET /drip/{duration}?interval=10s` | Sends status and headers right away, then one line every `interval` until `duration` has passed. | Whether the limit is on silence or on total request time. |
| `GET /healthz` | Answers 200. | |

`duration` and `interval` take plain seconds (`300`, `1.5`) or Go durations (`90s`, `5m`, `1h30m`).

With an interval longer than the duration (`/drip/600?interval=1h`), the server sends headers right away and then nothing until the end.

Add `probe=<name>` to any request and the name shows up in the server logs, so you can match a client run with what the server saw.

## Reading the results

When something between the client and the server cuts a request, the server logs `caller gave up` with how long it had been waiting (`after`). That number is the limit as seen from the backend, even when the client only sees a dropped connection.

On the client side, the shape of the failure tells you which layer cut it:

- **504 or 502 with a proxy error page**: the proxy (Traefik) cut it.
- **No status at all** (`curl: (52) Empty reply from server`, `(56) Connection reset by peer`): a load balancer, NAT or firewall dropped the connection.
- **The client's own timeout error** (`curl: (28)`): the client gave up first.

If `/hold/N` fails but `/drip/N` with a short interval gets through, the limit is on silence, and sending bytes keeps a request alive. If both fail at the same time, the limit is on total request time.

The server has no write timeout of its own. Any cut below `MAX_HOLD` comes from something outside the server.

Test in this order so you know which layer each limit belongs to:

1. From inside the cluster, straight against the Service.
2. Through the ingress, from inside the cluster.
3. From outside, the way real clients connect.

## Config

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Port to listen on. |
| `MAX_HOLD` | `30m` | Longest wait a caller may ask for. Longer requests get 400. |
| `BASE_PATH` | empty | Path prefix to remove before routing, for when the ingress forwards `/core/gateway-timeout/hold/60` as is. `/healthz` also moves under it. |

Logs are JSON on stdout.

## Run locally

```bash
go run .
curl localhost:8080/hold/5
curl -N 'localhost:8080/drip/30?interval=2'
```

```bash
go test ./...
```

```bash
docker build -t fint-core-gateway-timeout .
docker run --rm -p 8080:8080 -e MAX_HOLD=1h fint-core-gateway-timeout
```

## Deploy

Every push to `main` runs the tests, pushes `ghcr.io/fintlabs/fint-core-gateway-timeout:sha-<short sha>` (and `:latest`), and deploys to **alpha** (`aks-alpha-fint-2021-11-18`, namespace `fint-core`). A redeploy without a new commit can be started from the Actions tab (`workflow_dispatch`).

The deploy unit is a FLAIS `Application` in `kustomize/base`. The alpha overlay sets the route host, so the server answers on:

```
https://alpha.felleskomponent.no/core/gateway-timeout/hold/{duration}
https://alpha.felleskomponent.no/core/gateway-timeout/drip/{duration}
```

The route uses the same host as the real FINT services on purpose, so the probe goes through the same network gateway and Traefik as real traffic. The ingress does not strip `/core/gateway-timeout`, which is why `BASE_PATH` is set.

`MAX_HOLD` is 10m in the cluster. The endpoint has no auth, and every held request keeps a connection open through the shared gateway, so keep the cap low and remove the deployment when the testing is done.

## Probe script

`probe.sh` runs one request per duration and prints status and timings on one line each.

```bash
./probe.sh https://<host>/core/gateway-timeout 30 60 120 240 300
./probe.sh -m drip -i 10 https://<host>/core/gateway-timeout 300 600 1200
./probe.sh -n https://<host>/core/gateway-timeout 240 300
```

| Flag | Meaning |
|---|---|
| `-m hold\|drip` | Which endpoint to call. Default `hold`. |
| `-i <interval>` | Drip interval. Default `10`. |
| `-n` | Turn off curl's TCP keepalive. |

By default curl sends a TCP keepalive packet every 60 seconds. That can keep a load balancer's idle timer from ever firing, and the clients you actually care about may not send them. Run with and without `-n` and compare.

The script tags every request with `probe=<time>-<mode>-<duration>`, so each output line can be found in the server logs.
