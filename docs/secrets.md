# Share secrets with remote machines (nodes)

If you need a remote server to decrypt your encrypted configs (e.g. during `sysfig deploy`), register it as a node with its own [age](https://age-encryption.org/) key. sysfig will automatically re-encrypt all secrets for every registered node on the next `sync`.

## 1. Generate a key on the remote server

```sh
# On the remote server
age-keygen -o ~/.sysfig/keys/server.key
# Public key: age1abc123...
```

## 2. Register the node on your local machine

```sh
sysfig node add myserver age1abc123...
```

## 3. Re-encrypt and push

```sh
sysfig sync --push --message "add myserver node"
```

sysfig re-encrypts every secret for both your master key **and** the server's public key. Each machine can only decrypt using its own key.

## 4. Deploy to the server

```sh
sysfig deploy --host user@myserver git@github.com:you/conf.git
```

The server decrypts secrets with its `~/.sysfig/keys/server.key`. Your master key never leaves your machine.

## Manage nodes

```sh
sysfig node list             # show all registered nodes
sysfig node remove myserver  # unregister — re-encrypt single-recipient on next sync
```

> After `node remove`, run `sysfig sync --push` to re-encrypt secrets back to single-recipient. The removed server will get `age: no identity matched any of the recipients` on its next decrypt attempt.
