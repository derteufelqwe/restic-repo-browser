# Restic Repo Browser

A small web interface for inspecting your [restic](https://restic.net) repositories.
It is meant to run in your local network and not be exposed to the internet.

## Features

- [x] View and explore the snapshots of your restic repos
- [x] Local repo support
- [x] Rclone repo support
- [ ] Restic-rest repo support
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
./build.sh                                          # Windows: .\build.ps1 - all platforms into build/
# or just for this machine:
go build -o restic-browser ./cmd/restic-browser

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
| `--browse-dir` | home folder | Folder the **Browse** button of the repository dialog opens first. It must exist, otherwise the start fails. |
| `--secure-cookies` | off | Mark the session cookie `Secure` (when served over HTTPS). |
| `--debug` | off | Log every request. |
| `--log-level` | `default` | How much restic traffic is logged: `default` (as before), `commands` (every restic command line with its exit status and duration) or `responses` (also what restic printed, each stream cut at 8 KiB). |

`--log-level` is for finding out why restic behaves differently than expected. The log never contains
repository passwords, and the data of `restic dump` (downloaded files) is never logged. Responses can contain
file and folder names, so treat the log accordingly.

`secret.key` encrypts the repository passwords inside the database. Keep it next to the database
and back both up together; the database alone does not reveal the passwords. The login password
is stored as a bcrypt hash. The server speaks plain HTTP, so put it behind a TLS proxy if the
network is not trusted.

## Run it as a service (systemd)

[`deploy/restic-browser.service`](deploy/restic-browser.service) runs the binary as its own user and keeps
its data in `/var/lib/restic-browser`.

```sh
sudo install -m 755 restic-browser /usr/local/bin/restic-browser        # restic must be installed too
sudo useradd --system --home-dir /var/lib/restic-browser --shell /usr/sbin/nologin restic-browser
sudo install -m 644 deploy/restic-browser.service /etc/systemd/system/

sudo install -d -o restic-browser -g restic-browser -m 700 /var/lib/restic-browser
sudo -u restic-browser restic-browser set-password --data-dir /var/lib/restic-browser   # login password

sudo systemctl daemon-reload
sudo systemctl enable --now restic-browser
journalctl -u restic-browser -f                                         # logs
```

Edit the `ExecStart` line first to set the listen address and `--browse-dir`. The service user needs read access
to your repositories, and write access if you use cleanup jobs (forget and prune), for example
`sudo chown -R restic-browser /srv/restic`. After changing the unit run `sudo systemctl daemon-reload && sudo systemctl restart restic-browser`.


## Disclaimer

AI was involved in developing this tool (which, in 2026, is a codeword for "It was fully vibe coded").
But it does work very well for me, so it might also work fine for you. If not you are welcome to open a PR or create an issue.
