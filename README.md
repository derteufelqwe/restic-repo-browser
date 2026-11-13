# Restic Repo Browser

A small web interface for looking at [restic](https://restic.net) repositories on local disks.
It is meant to run in your local network.

## Features

- [x] View and explore the snapshots of your local restic repos
- [x] Simple single-user login
- [x] Data is cached in case your resic repos are on cold storage (like a spun down HDD)
- [x] Configure automatic pruning jobs (this is the only non-viewing part)
- [x] Periodic cache updates
- [x] Browse / download invidual snapshots


## Download

Ready-made binaries for Linux, Windows and macOS (amd64 and arm64) are on the
[Releases](../../releases) page. Unpack one, install [restic](https://restic.net), and continue below.

Releases are built by GitHub Actions: pushing a tag such as `git tag v0.1.0 && git push origin v0.1.0`
automatically.

## Run it

```sh
go build -o restic-browser ./cmd/restic-browser     # or: GOOS=linux go build ...

./restic-browser set-password                       # asks for the login password
./restic-browser --listen :8080 --data-dir /var/lib/restic-browser
```

Then open `http://<server>:8080`, sign in and add repositories under *Settings*
(name, folder path, repository password). The path field has a **Browse** button that lists the
folders of the server so you do not have to type the path.

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:8080` | Address to listen on. Use `:8080` to serve the whole network. |
| `--data-dir` | `data` | Holds `restic-browser.db`, `secret.key` and restic's cache. |
| `--restic` | `restic` | restic executable (looked up on `PATH`, then next to the binary). |
| `--secure-cookies` | off | Mark the session cookie `Secure` (when served over HTTPS). |
| `--debug` | off | Log every request. |

`secret.key` encrypts the repository passwords inside the database. Keep it next to the database
and back both up together; the database alone does not reveal the passwords. The login password
is stored as a bcrypt hash. The server speaks plain HTTP, so put it behind a TLS proxy if the
network is not trusted.

## Try it with demo data

```sh
go run ./tools/mkdemo -out demo -restic ./restic.exe     # builds real restic repos, takes ~1 minute
./restic-browser --data-dir demo/data --restic ./restic.exe
# sign in with: demo-password
```


## Disclaimer

AI was involved in developing this tool (which in 2026 is a codeword for "It was absolutely vibe coded").
But it does work very well for me, so it might also work fine for you. If not you are welcome to open a PR.
