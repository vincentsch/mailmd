#!/usr/bin/env bash
# Run the integration tests against real Dovecot and GreenMail servers in
# Docker. Needs Docker, OpenSSL and Go. CI also runs this script.
set -eu
cd "$(dirname "$0")/.."

work=$(mktemp -d)
cleanup() {
    docker rm -f mailmd-dev-it-dovecot mailmd-dev-it-greenmail >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT INT TERM

# A small CA and a certificate for localhost, so Dovecot's TLS can be checked
# like a real server's.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 \
    -subj "/CN=mailmd test CA" -keyout "$work/ca.key" -out "$work/ca.pem" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -subj "/CN=localhost" -keyout "$work/srv.key" -out "$work/srv.csr" 2>/dev/null
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' > "$work/ext.cnf"
openssl x509 -req -in "$work/srv.csr" -CA "$work/ca.pem" -CAkey "$work/ca.key" -CAcreateserial \
    -days 2 -extfile "$work/ext.cnf" -out "$work/srv.pem" 2>/dev/null
chmod 644 "$work/srv.key"

docker run -d --name mailmd-dev-it-dovecot -p 127.0.0.1:13993:993 -p 127.0.0.1:13143:143 \
    -v "$work:/etc/dovecot/tls:ro" \
    -v "$PWD/internal/integration/testdata/dovecot.conf:/etc/dovecot/dovecot.conf:ro" \
    dovecot/dovecot:2.3.21 >/dev/null
docker run -d --name mailmd-dev-it-greenmail -p 127.0.0.1:14143:3143 greenmail/standalone:2.1.2 >/dev/null

wait_port() {
    for _ in $(seq 1 60); do
        if (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; then return 0; fi
        sleep 1
    done
    echo "Port $1 did not open." >&2
    exit 1
}
wait_port 13993
wait_port 14143
sleep 2

MAILMD_IT_CA="$work/ca.pem" \
MAILMD_IT_DOVECOT=localhost:13993 \
MAILMD_IT_STARTTLS=localhost:13143 \
MAILMD_IT_GREENMAIL=127.0.0.1:14143 \
    go test -tags integration -count=1 -v ./internal/integration/
