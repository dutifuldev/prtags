# GCP Deploy Setup

The GCP deployment mounts the GitHub App private key from `/home/bob/prtags/secrets` into the `prtags` container.

Use these ownership and mode settings on the VM before restarting the service:

```bash
sudo chown 10001:10001 /home/bob/prtags/secrets
sudo chmod 0500 /home/bob/prtags/secrets
sudo chown 10001:10001 /home/bob/prtags/secrets/github-app.private-key.pem
sudo chmod 0400 /home/bob/prtags/secrets/github-app.private-key.pem
```

Keep the app env file pointing at that mounted key path:

```env
GITHUB_APP_PRIVATE_KEY_PATH=/home/bob/prtags/secrets/github-app.private-key.pem
```

The shared Cloud SQL instance is connection-limited, so keep the default pool settings conservative unless you have intentionally split workers or moved the database:

```env
DB_MAX_OPEN_CONNS=5
DB_MAX_IDLE_CONNS=2
DB_CONN_MAX_IDLE_TIME=5m
DB_CONN_MAX_LIFETIME=30m
```
