#!/usr/bin/env bash
# make-selfsigned-codesign.sh <outdir> [ec|rsa] writes a throwaway Authenticode
# code-signing identity: codesign.p12 (key and certificate, for Jsign's PKCS12
# store) and codesign.crt (the public certificate, for a trust store). It
# exists to exercise the signing path before the HSM key and its CA
# certificate do; nothing signed with it is shippable. It never touches the
# release workflow.
#
# ec (the default) is a P-256 key, the class of key the Cloud HSM plan uses,
# so a green run also shows Jsign and Windows accept it. rsa is the fallback
# if the CA will not issue against an EC key.
#
# Required environment:
#   CODESIGN_P12_PASSWORD  password to protect codesign.p12 with
set -euo pipefail

outdir=${1:?usage: make-selfsigned-codesign.sh <outdir> [ec|rsa]}
alg=${2:-ec}
: "${CODESIGN_P12_PASSWORD:?set to the password for codesign.p12}"

umask 077
mkdir -p "$outdir"
key="$outdir/codesign.key"
config="$outdir/codesign.cnf"
trap 'rm -f "$key" "$config"' EXIT

# A config file rather than -addext, which LibreSSL (macOS) lacks.
cat > "$config" <<'EOF'
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = Aperture Test Signing
O = Aperture Test (self-signed)
[ext]
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = critical, codeSigning
EOF

case "$alg" in
  ec) openssl ecparam -genkey -name prime256v1 -noout -out "$key" ;;
  rsa) openssl genrsa -out "$key" 3072 2>/dev/null ;;
  *) echo "unknown key algorithm $alg: want ec or rsa" >&2; exit 2 ;;
esac

openssl req -new -x509 -days 7 -sha256 -config "$config" -key "$key" -out "$outdir/codesign.crt"
CODESIGN_P12_PASSWORD="$CODESIGN_P12_PASSWORD" openssl pkcs12 -export \
  -inkey "$key" -in "$outdir/codesign.crt" -name codesign \
  -passout env:CODESIGN_P12_PASSWORD -out "$outdir/codesign.p12"
echo "wrote $outdir/codesign.p12 and $outdir/codesign.crt ($alg, expires in 7 days)"
