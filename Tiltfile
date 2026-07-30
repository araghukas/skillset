# Local dev loop for skillsd.

# Prerequisites (one-time):
#   `ctlptl apply -f local/cluster.yaml`

# Then:
#   `tilt up`

# OR, just run the following to start the cluster AND do Tilt up:
#   `made dev`

# Optional private-repo auth for the read-only skillsd fleet: drop a
# fine-grained GitHub token scoped read-only ("Contents: read") to the repo
# configured at skillsRepo.url in local/values.yaml at
#   local/git-skillsd-token
#
# (gitignored) and this Tiltfile will create a Secret from it and point
# charts/skillsd's skillsRepo.tokenSecret at it automatically.

# Optional skillsd-registry (proposal/PR write path): drop a GitHub token
# with push + pull-request write access on the repo configured at
# registry.skillsRepo.url / registry.github.owner+repo in local/values.yaml
# at
#   local/git-skillsd-registry-token
#
# (gitignored) and this Tiltfile will create a Secret from it, enable
# registry.enabled, and point registry.github.tokenSecret at it
# automatically.

allow_k8s_contexts('kind-skillsd')

docker_build(
    'localhost:5005/skillsd',
    '.',
    dockerfile='Dockerfile',
    ignore=['README.md', 'Makefile', '.gitignore'],
)

load("ext://base64", "encode_base64")

# Read-only repo auth: create a Secret from local/git-skillsd-token, if present

git_secret_name = ''
if os.path.exists('local/git-skillsd-token'):
    git_secret_name = 'skillsd-git-auth'
    k8s_yaml(blob('''
apiVersion: v1
kind: Secret
metadata:
  name: {name}
type: Opaque
data:
  token: {token}
'''.format(
        name=git_secret_name,
        token=encode_base64(str(read_file('local/git-skillsd-token')).rstrip()),
    )))

helm_set = []
if git_secret_name:
    helm_set.append('skillsRepo.tokenSecret=' + git_secret_name)

# Registry write path: create a Secret from local/git-skillsd-registry-token, if present

registry_secret_name = ''
if os.path.exists('local/git-skillsd-registry-token'):
    registry_secret_name = 'skillsd-registry-git-auth'
    k8s_yaml(blob('''
apiVersion: v1
kind: Secret
metadata:
  name: {name}
type: Opaque
data:
  token: {token}
'''.format(
        name=registry_secret_name,
        token=encode_base64(str(read_file('local/git-skillsd-registry-token')).rstrip()),
    )))
    helm_set.append('registry.enabled=true')
    helm_set.append('registry.github.tokenSecret=' + registry_secret_name)

# Install the skillsd chart, wiring in any secrets created above

k8s_yaml(helm(
    'charts/skillsd',
    name='skillsd',
    values=['local/values.yaml'],
    set=helm_set,
))

k8s_resource(
    'skillsd',
    port_forwards=['8080:8080'],
)

if registry_secret_name:
    k8s_resource(
        'skillsd-registry',
        port_forwards=['8081:8081'],
    )
