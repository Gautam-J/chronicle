#!/bin/bash
# Ships the code of this repository on the Pi: pull, test, build, start the
# service again, then check the public hostname through Cloudflare.
# Run it on the Pi as the user of the service: deploy/deploy.sh
set -euo pipefail

cd "$(dirname "$0")/.."
set -a; . deploy/.env; set +a

git pull --ff-only
make test
# The service runs the old binary until the restart.
make build

# The unit file is a link into this folder: a pull can change it.
unit=chronicle.service
if [ ! -L "/etc/systemd/system/$unit" ]; then
    sudo ln -s "$PWD/deploy/systemd/$unit" "/etc/systemd/system/$unit"
    sudo systemctl daemon-reload
    sudo systemctl enable "$unit"
fi
sudo systemctl daemon-reload
sudo systemctl restart "$unit"

# A GET on the ingest path answers 405 once the server listens.
up=
for _ in $(seq 15); do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' "http://$CHRONICLE_ADDR/owntracks/pub")" = 405 ]; then
        up=1
        break
    fi
    sleep 1
done
if [ -z "$up" ]; then
    echo "chronicle does not answer on $CHRONICLE_ADDR: see journalctl -u chronicle" >&2
    exit 1
fi
echo "chronicle answers on $CHRONICLE_ADDR"

# Through Cloudflare: Access guards the hostname, Basic auth guards the ingest path.
host=https://chronicle.neoquant.trade
check() {
    local got
    got=$(curl -s -o /dev/null -w '%{http_code}' "${@:3}" "$host$2")
    if [ "$got" = "$1" ]; then
        echo "ok   $1 $2"
    else
        echo "FAIL $2: got $got, want $1" >&2
        return 1
    fi
}
check 302 /
check 401 /owntracks/pub -d '{}'
check 302 /owntracks/pubx -d '{}'
