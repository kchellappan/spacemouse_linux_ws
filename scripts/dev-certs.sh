#!/usr/bin/env bash
# Generate a local CA and a leaf certificate for 127.51.68.120.
#
# The client library hardcodes that IP (3dconnexion.js:103), so the leaf MUST
# carry subjectAltName = IP:127.51.68.120 and no public CA will ever issue it.
#
# Gecko requires a genuine CA -> leaf chain: a single self-issued CA:TRUE cert
# used as its own end-entity is rejected. See docs/04-certificates-and-browser-trust.md.
set -euo pipefail

OUT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/certs}"
IP="127.51.68.120"

mkdir -p "$OUT"
cd "$OUT"

if [[ -f ca.pem && -f ca-key.pem ]]; then
  echo "reusing existing CA in $OUT"
else
  echo "generating CA…"
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -subj "/CN=SpaceMouse Bridge Local CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -keyout ca-key.pem -out ca.pem 2>/dev/null
fi

echo "generating leaf for $IP…"
openssl req -newkey rsa:2048 -nodes -subj "/CN=$IP" \
  -keyout leaf-key.pem -out leaf.csr 2>/dev/null

openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
  -days 825 -out leaf.pem \
  -extfile <(printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=IP:%s\n' "$IP") 2>/dev/null

cat leaf.pem ca.pem > fullchain.pem
rm -f leaf.csr
chmod 600 ca-key.pem leaf-key.pem

echo
openssl verify -CAfile ca.pem leaf.pem
echo
echo "wrote to $OUT:"
echo "  ca.pem         install this into browser trust stores (scripts/trust-certs.sh)"
echo "  fullchain.pem  serve this  (-cert)"
echo "  leaf-key.pem   serve this  (-key)"
