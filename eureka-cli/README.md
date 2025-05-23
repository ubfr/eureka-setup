# Eureka CLI

## Purpose

- A CLI to deploy local Eureka development environment

## Commands

### Prerequisites

- Install dependencies:
  - [GO](<https://go.dev/doc/install>) compiler: last development-tested version is `go1.24.1 windows/amd64`
  - [Rancher Desktop](<https://rancherdesktop.io/>) container daemon: last development-tested version is `v1.16.0` (make sure to enable **dockerd (Moby)** container engine)
- Configure hosts:
  - Add `127.0.0.1 keycloak.eureka` entry to `/etc/hosts`
  - Add `127.0.0.1 kafka.eureka` entry to `/etc/hosts`
- Monitor using system components:
  - [Keycloak](<http://keycloak.eureka:8080>) Admin Console: admin:admin
  - [Vault](<http://localhost:8200>) UI: Find a Vault root token in the container logs using `docker logs vault` or use `getVaultRootToken` command
  - [Kafka](<http://localhost:9080>) UI: No auth
  - [Kong](<http://localhost:8002>) Admin GUI: No auth  

### Build a binary
  
```shell
mkdir -p ./bin
env GOOS=windows GOARCH=amd64 go build -o ./bin .
```

> See docs/BUILD.md to build a platform-specific binary

### (Optional) Setup a default config in the home folder

- This config will be used by default if `-c` or `--config` flag is not specified

```shell
./bin/eureka-cli setup
```

### (Optional) Install binary

- After building and installing the binary can be used from any directory

```shell
go install
eureka-cli -c ./config.combined.yaml deployApplication
```

### (Optional) Enable autocompletion

- Command autocompletion can be enabled in the shell of your choice, below is an example for the **Bash** shell

```bash
go install
echo "source <(eureka-cli completion bash)" >> ~/.bashrc
source ~/.bashrc
```

> After typing the command partially and hitting the TAB key, the command will autocomplete, e.g. `eureka-cli intercept` + TAB key will result in `eureka-cli interceptModule`

### Deploy the combined application with Acquisitions modules

#### Using Public DockerHub container registry (folioci & folioorg namespaces)

- Use a specific config: `-c` or `--config`
- Enable debug: `-d` or `--debug`

```shell
./bin/eureka-cli -c ./config.combined.yaml deployApplication
```

#### Using Private AWS ECR container registry

To use AWS ECR as your container registry rather than the public Folio DockerHub, set `AWS_ECR_FOLIO_REPO` in your environment. When this env variable is defined it is assumed that this repository is private and you have also defined credentials in your environment. The value of this variable should be the URL of your repository.

- Set AWS credentials explicitly

```shell
export AWS_ACCESS_KEY_ID=<access_key>
export AWS_SECRET_ACCESS_KEY=<secret_key>
export AWS_ECR_FOLIO_REPO=<repository_url> 
./bin/eureka-cli -c ./config.combined.yaml deployApplication
```

- Reuse stored AWS credentials found in `~/.aws/config`

```shell
export AWS_ECR_FOLIO_REPO=<repository_url>
AWS_SDK_LOAD_CONFIG=true ./bin/eureka-cli. -c ./config.combined.yaml deployApplication
```

> See docs/AWS_CLI.md to prepare AWS CLI beforehand

### Undeploy the combined application

```shell
./bin/eureka-cli -c ./config.combined.yaml undeployApplication
```

### Use the environment

- Access the UI from `http://localhost:3000` using `diku_admin` username and `admin` password:

![UI](images/ui_form.png)

- Kong gateway is available at `localhost:8000` and can be used to get an access token directly from the backend:

```shell
# Using diku_admin (admin user)
curl --request POST \
  --url localhost:8000/authn/login-with-expiry \
  --header 'Content-Type: application/json' \
  --header 'X-Okapi-Tenant: diku' \
  --data '{"username":"diku_admin","password": "admin"}' \
  --verbose

# Using diku_user (limited user)
curl --request POST \
  --url localhost:8000/authn/login-with-expiry \
  --header 'Content-Type: application/json' \
  --header 'X-Okapi-Tenant: diku' \
  --data '{"username":"diku_user","password": "user"}' \
  --verbose
```

### Troubleshooting

#### General

- If using Rancher Desktop on a system that also uses Docker Desktop make sure to set `DOCKER_HOST` to point to the correct container daemon, by default `/var/run/docker.sock` will be used

#### Command-based

- If during `Deploy System` or `Deploy Ui` shell commands are failing to execute verify that all shell scripts located under `./misc` folder are saved using the **LF** (Line Feed) line break
- If during `Deploy Management` or `Deploy Modules` the healthchecks are failing make sure to either define **host.docker.internal** in `/etc/hosts` or set `application.gateway-hostname=172.17.0.1` in the `config.*.yaml`
- If during `Deploy Modules` an exception contains **"Bind for 0.0.0.0:XXXXX failed: port is already allocated."** make sure to set `application.port-start=20000` in the `config.*.yaml`
- If during `Deploy Modules` an exception contains **"Failed to load module descriptor by url: <https://folio-registry.dev.folio.org/_/proxy/modules/mod-XXX>"** make sure that the module descriptor for this version exists or use an older module version by setting `mod-XXX.version` in the `config.*.yaml`
- If during `Create Tenant Entitlement` an exception contains **"The module is not entitled on tenant ..."** rerun `undeployApplication` and `deployApplication` once again with more available RAM
