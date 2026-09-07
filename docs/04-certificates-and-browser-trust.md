# Certificates and Browser Trust

This was the hardest part of the investigation and the only real install
friction. It is now solved and validated. Read this before writing the
installer.

## Why a local trust anchor is unavoidable

`3dconnexion.js:103` hardcodes `this.host = "127.51.68.120"`. You cannot
substitute a hostname, so the `*.plex.direct` trick — a public DNS name with a
real certificate pointed at loopback — **is not available**.

The certificate must therefore carry:

```
subjectAltName = IP:127.51.68.120
```

No public CA will ever issue that; CA/Browser Forum baseline requirements
prohibit certificates for reserved IP ranges. A locally-installed trust anchor
is the only path. This is exactly why 3Dconnexion's own Windows installer
generates a CA into the system store.

`https://` is likewise mandatory: the host page is HTTPS, so `ws://` would be
blocked as mixed content.

## What did not work, and why

Three attempts, in order. The failures are instructive.

### Attempt 1 — self-signed leaf, NSS trust `P,,`

```sh
openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
  -subj "/CN=127.51.68.120" -addext "subjectAltName=IP:127.51.68.120" ...
certutil -d sql:<profile> -A -n "…" -t "P,," -i cert.pem
```

| Browser | Result |
|---|---|
| Chrome | **works** — honours `P,,` (trusted peer) for a self-signed leaf |
| Zen (Gecko) | fails silently — no request reaches the server at all |

`P,,` is attractive because it authorises exactly one certificate as a server
identity rather than minting a CA. Chrome accepts it. Gecko does not.

### Attempt 2 — same certificate, trust widened to `C,,`

Still failed on Gecko. Two faults were present simultaneously, so this run was
inconclusive: the browser was still running during injection (**NSS reads its
store at startup**, and Flatpak/snap keep content processes alive after the
window closes), *and* the certificate was structurally wrong.

### Why Gecko rejected it

Two separate discoveries.

**1. Gecko ignores `P,,` for server certificates.** Firefox and its forks accept
a server cert only if it chains to a trusted **CA**, or if there is an exact
entry in `cert_override.txt`. That file is where manual click-through exceptions
land — a completely different mechanism from NSS trust flags:

```
# ~/.var/app/app.zen_browser.zen/.zen/<profile>/cert_override.txt
127.51.68.120:8181:	OID.2.16.840.1.101.3.4.2.1	DC:4D:C2:DF:…:02:25
```

That fingerprint was confirmed to match the earlier click-through cert exactly,
and not the injected one. `OID.2.16.840.1.101.3.4.2.1` is SHA-256.

**2. The certificate was its own trust anchor.**

```
Issuer:  CN = 127.51.68.120
Subject: CN = 127.51.68.120     ← self-issued
         CA:TRUE                 ← and a CA
```

mozilla::pkix wants a genuine anchor→leaf chain with `CA:FALSE` on the
end-entity. **This is why mkcert generates a separate root and leaf** — it is a
Gecko requirement, not a stylistic choice.

## What works

A per-user **CA** plus a distinct **leaf**, with the CA installed into the trust
store and the leaf served as a chain.

```sh
# CA — this goes into the trust stores
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
  -subj "/CN=SpaceMouse Bridge Local CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -keyout ca-key.pem -out ca.pem

# Leaf — this is what the server presents
openssl req -newkey rsa:2048 -nodes -subj "/CN=127.51.68.120" \
  -keyout leaf-key.pem -out leaf.csr

openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
  -days 825 -out leaf.pem \
  -extfile <(printf "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=IP:127.51.68.120\n")

cat leaf.pem ca.pem > fullchain.pem
chmod 600 ca-key.pem leaf-key.pem
```

Verification, strict, no `-k`:

```
$ curl --cacert ca.pem https://127.51.68.120:8181/3dconnexion/nlproxy
{"port": 8181, "version": "1.4.8.21486"}         exit=0

 0 s:CN = 127.51.68.120                ← leaf, CA:FALSE
 1 s:CN = SpaceMouse Bridge Local CA   ← issuer
Verify return code: 0 (ok)
```

## Trust injection

One mechanism for every browser: find `cert9.db`, inject the **CA** with
`certutil -A -t "C,,"`.

```sh
certutil -A -d sql:"<profile-dir>" -n "SpaceMouse Bridge Local CA" -t "C,," -i ca.pem
certutil    -d sql:"<profile-dir>" -L | grep -i spacemouse    # expect C,,
```

### Store locations — all validated 2026-09-06

| Engine | Packaging | Path | Verified |
|---|---|---|---|
| Chromium | deb | `~/.pki/nssdb` | ✅ |
| Gecko | snap (Firefox) | `~/snap/firefox/common/.mozilla/firefox/<profile>/` | ✅ |
| Gecko | flatpak (Zen) | `~/.var/app/app.zen_browser.zen/.zen/<profile>/` | ✅ |
| Gecko | deb (Firefox) | `~/.mozilla/firefox/<profile>/` | not tested — strict subset of the confined cases |

For the installer, **glob rather than hardcode browser names**. This picks up
LibreWolf, Floorp, Waterfox and future Flatpaks for free:

```
~/.pki/nssdb                              # Chromium family (single shared db)
~/.mozilla/firefox/*/                     # Gecko, deb
~/snap/*/common/.mozilla/firefox/*/       # Gecko, snap
~/.var/app/*/.mozilla/firefox/*/          # Gecko, flatpak
~/.var/app/*/.zen/*/                      # Zen, flatpak
```

### Confinement is not an obstacle

Snap and Flatpak profile data lives at ordinary **host** paths owned by the
user. Confinement governs what the *application* can see, not what the host can
write into its data directory. Host-side `certutil` reaches all of them.

### Gotchas

- **`~/.pki/nssdb` may not exist.** Clicking through Chrome's interstitial
  stores a per-profile SSL decision, *not* an NSS entry. Create it:
  `certutil -N --empty-password -d sql:"$HOME/.pki/nssdb"`.
- **Browsers must be fully stopped during injection.** NSS reads at startup, and
  Flatpak/snap keep content processes alive after the window closes. Verify with
  `pgrep`, don't assume. Inject at login, before browsers start.
- **Resolve the active profile via `profiles.ini`.** Zen had two profile
  entries; the live one was named by an `[InstallXXXX]` section with
  `Default=… Locked=1`.
- Gecko's `cert_override.txt` pins host:port + fingerprint. A *fresh*
  certificate therefore bypasses any stale click-through exception — useful for
  designing an honest test.

### Fallback if `certutil` ever fails on a confined Gecko

`snap connections firefox` shows `system-files firefox:etc-firefox` connected,
so snap Firefox **can** read `/etc/firefox/policies/policies.json`, and
`Certificates.Install` is available there. Not needed today; recorded in case it
becomes so.

## Security requirements — non-negotiable

**Never ship a private key in the package.** `spacenav-ws` commits
`certs/ip.key` to its repository — acceptable for a proof of concept,
disqualifying for distribution. If a CA is installed into users' trust stores
and its key is extractable from a public `.deb`, anyone able to MITM the
network can forge certificates for *any* site. That is the Superfish /
eDellRoot failure mode.

Requirements:

- Generate the CA **per machine, per user, at first run** of the user service —
  not in `postinst`, which cannot reach per-user NSS stores anyway.
- Store under `~/.local/share/spacemouse-bridge/`, mode **600**, user-owned.
- Never transmit it.
- Have `selftest` verify file permissions.

With those in place the residual risk is small: an attacker who can read that
key can already read the same profile's cookies and session tokens.

## Leaf expiry

The leaf above is 825 days. The service should check expiry at startup and
reissue from the CA. The CA itself can live much longer (10 years above).

## Reference implementation

[mkcert](https://github.com/FiloSottile/mkcert) solves exactly this problem, is
written in **Go**, and is BSD-3 licensed. Its `truststore` package already
handles NSS profile discovery, `certutil` invocation, and the system store on
Linux. Vendoring or cribbing it saves rediscovering every quirk above; you would
add the Zen/Flatpak/snap roots to its search paths.
