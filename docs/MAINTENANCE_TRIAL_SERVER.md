# Isolated kbls expiry drill

The production workflow should close successfully before running the expiry
drill in a **new session** against an isolated server. Do not switch an active
production session between databases: report sequences and release proofs belong
to one server, and closing a copied session does not close the production session.

The server operator performs the VPS steps below. The production unit, database,
approval windows and contract dates must remain unchanged. Use a private database
snapshot and a different listen address; `kbls start`/`restart` must not be used,
because they can manage the production systemd unit.

## Prepare the private instance

Run as the existing server user after the production trial has closed:

```sh
umask 077
trial_dir="$HOME/.local/share/kbls-maintenance-trial"
mkdir -p "$trial_dir"
chmod 700 "$trial_dir"
/home/frappe/go/bin/kbls backup --out "$trial_dir/licenses.db"
```

The backup command refuses an existing destination. Preserve earlier evidence
instead of overwriting or deleting a database. This snapshot contains license
keys and customer licensing records; keep it private on the trusted VPS.

Create the trial config without printing its secrets:

```sh
python3 - "$HOME/.config/kbls/config.json" "$trial_dir" <<'PY'
import json, os, pathlib, sys
production_path = pathlib.Path(sys.argv[1])
trial = pathlib.Path(sys.argv[2])
config = json.loads(production_path.read_text())
database = trial / "licenses.db"
assert database.is_file()
assert not os.path.samefile(database, pathlib.Path(config["db_path"]).expanduser())
key = pathlib.Path(config["signing_key_path"]).expanduser()
assert key.is_absolute() and key.is_file()
config["signing_key_path"] = str(key)
config["db_path"] = str(database)
config["listen_addr"] = "127.0.0.1:32790"
destination = trial / "config.json"
fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "w") as output:
    json.dump(config, output, indent=2)
    output.write("\n")
    output.flush()
    os.fsync(output.fileno())
print("Private trial config created; production config unchanged")
PY
KBLS_CONFIG_DIR="$trial_dir" /home/frappe/go/bin/kbls verify-db --db "$trial_dir/licenses.db"
KBLS_CONFIG_DIR="$trial_dir" /home/frappe/go/bin/kbls serve
```

The last command runs in the foreground. This config uses the existing signing
key on the same trusted VPS so the current candidate can verify its JWTs. Do not
copy that private key elsewhere. A separate signing key instead requires a
separate trial CLI build with its public key. Keep the instance on loopback and
provide an authenticated private route or appropriate TLS route to the trial
bench. Report only the endpoint and readiness, never a PAT, password or key.

### Private routing for the disposable acme bench

On the outer KB host, use the operator's existing VPS SSH login to forward only
on acme's private Docker bridge gateway. Do not bind on `0.0.0.0` or a public
interface. Keep this SSH process in the foreground in a separate terminal:

```sh
trial_bridge=$(docker inspect --format '{{(index .NetworkSettings.Networks "ffm-acme_default").Gateway}}' ffm-acme-frappe-1)
test -n "$trial_bridge"
ssh -N -o ExitOnForwardFailure=yes \
  -L "$trial_bridge:32790:127.0.0.1:32790" <your-existing-VPS-SSH-target>
```

The verified acme gateway on 2026-10-13 is `172.26.0.1`. Re-read it after a
container/network recreation. Inside `ffm shell acme`, check only health:

```sh
curl -fsS http://172.26.0.1:32790/health
```

Use `KB_LICENSE_SERVER=http://172.26.0.1:32790` for the separate trial candidate
session. This route relies on the trusted disposable Docker bridge and the
authenticated SSH tunnel; it is not a public HTTP deployment. For the isolated
admin interface, open a separate workstation tunnel to VPS loopback:

```sh
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:32790:127.0.0.1:32790 <your-existing-VPS-SSH-target>
```

Then use `http://127.0.0.1:32790/admin` locally. Do not expose the admin port or
copy server credentials into the bench. Close both tunnels deliberately after
the isolated session is closed, and retain its private evidence.

## Create the separate trial session

Keep the production CLI installed. Use the staged candidate with
`KB_LICENSE_SERVER` set to the isolated endpoint for **every** command in this
new session. Request approval, approve it on the isolated admin interface and
enter with its new code at the no-echo prompt. The source must have returned to
archive before this request. Record the new session ID, both-site backups and
source manifest before changing the isolated approval window.

Finish the session cleanly with its base already contained in the selected
published release; no new trial support commit is needed for this expiry drill.

## Expire work and the contract in the trial database only

Use a separate VPS terminal. Set `trial_dir` as above and set `trial_session` to
the newly created isolated session, not the earlier production session:

```sh
trial_session="<32-hex isolated session ID>"
python3 - "$trial_dir/licenses.db" "$trial_session" <<'PY'
import datetime, json, os, pathlib, re, sqlite3, sys
path = pathlib.Path(sys.argv[1])
expected = pathlib.Path.home() / ".local/share/kbls-maintenance-trial/licenses.db"
assert path.resolve() == expected.resolve()
production = pathlib.Path.home() / ".config/kbls/config.json"
production_database = pathlib.Path(json.loads(production.read_text())["db_path"]).expanduser()
assert not os.path.samefile(path, production_database)
session = sys.argv[2]
assert re.fullmatch(r"[0-9a-f]{32}", session)
past = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(minutes=1)).isoformat().replace("+00:00", "Z")
with sqlite3.connect(path) as db:
    row = db.execute("SELECT client_id, closed_at FROM maintenance_sessions WHERE session_id = ?", (session,)).fetchone()
    assert row and row[1] is None
    client = row[0]
    original = db.execute("SELECT contract_end FROM clients WHERE client_id = ?", (client,)).fetchone()
    assert original
    record = path.parent / ("contract-before-" + session + ".json")
    fd = os.open(record, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump({"client_id": client, "contract_end": original[0]}, output)
        output.flush()
        os.fsync(output.fileno())
    db.execute("UPDATE maintenance_sessions SET work_until = ? WHERE session_id = ?", (past, session))
    db.execute("UPDATE clients SET contract_end = ? WHERE client_id = ?", (past, client))
print("Isolated work window and contract expired; production unchanged")
PY
```

The candidate must now refuse exit before source mutation. Record the source
and Git digests, error and offline status. Restore the original trial contract
while leaving the isolated work window expired:

```sh
python3 - "$trial_dir/licenses.db" "$trial_session" <<'PY'
import json, os, pathlib, re, sqlite3, sys
path = pathlib.Path(sys.argv[1])
expected = pathlib.Path.home() / ".local/share/kbls-maintenance-trial/licenses.db"
assert path.resolve() == expected.resolve()
production = pathlib.Path.home() / ".config/kbls/config.json"
assert not os.path.samefile(path, pathlib.Path(json.loads(production.read_text())["db_path"]).expanduser())
session = sys.argv[2]
assert re.fullmatch(r"[0-9a-f]{32}", session)
before = json.loads((path.parent / ("contract-before-" + session + ".json")).read_text())
with sqlite3.connect(path) as db:
    assert db.execute("SELECT client_id FROM maintenance_sessions WHERE session_id = ?", (session,)).fetchone() == (before["client_id"],)
    db.execute("UPDATE clients SET contract_end = ? WHERE client_id = ?", (before["contract_end"], before["client_id"]))
print("Isolated contract restored; work approval remains expired")
PY
```

Report against the isolated endpoint so its lazy expiry check records approval
expiry locally and in admin. Status must keep protection; entry must refuse.
Exit to the explicit containing release must succeed without a fresh approval,
and close must return 200. Record source recovery, per-site outcomes and import.
After isolated close, return commands to the production endpoint and confirm an
ordinary upgrade succeeds. Stop the foreground trial server deliberately and
retain the private snapshot/evidence according to the operator's retention
policy; this guide deletes nothing.
