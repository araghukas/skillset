# Local dev loop for skillsd.
#
# Prerequisites (one-time):
#   ctlptl apply -f local/cluster.yaml
#
# Then:
#   tilt up
#
# Optional private-repo auth for the read-only skillsd fleet: drop an SSH
# deploy key and known_hosts file at
#   local/git-deploy-key
#   local/git-known-hosts
# (both gitignored) and this Tiltfile will create a Secret from them and
# point charts/skillsd's skillsRepo.existingSecret at it automatically.
#
# Optional skillsd-registry (proposal/PR write path): drop a GitHub token
# with push + pull-request write access on the repo configured at
# registry.skillsRepo.url / registry.github.owner+repo in local/values.yaml
# at
#   local/github-token
# (gitignored) and this Tiltfile will create a Secret from it, enable
# registry.enabled, and point registry.github.tokenSecret at it
# automatically.

allow_k8s_contexts('kind-skillsd')

docker_build(
    'localhost:5005/skillsd',
    '.',
    dockerfile='Dockerfile',
)

load("ext://base64", "encode_base64")

git_secret_name = ''
if os.path.exists('local/git-deploy-key') and os.path.exists('local/git-known-hosts'):
    git_secret_name = 'skillsd-git-auth'
    k8s_yaml(blob('''
apiVersion: v1
kind: Secret
metadata:
  name: {name}
type: Opaque
data:
  ssh-privatekey: {key}
  known_hosts: {known_hosts}
'''.format(
        name=git_secret_name,
        key=encode_base64(read_file('local/git-deploy-key')),
        known_hosts=encode_base64(read_file('local/git-known-hosts')),
    )))

helm_set = []
if git_secret_name:
    helm_set.append('skillsRepo.existingSecret=' + git_secret_name)

registry_secret_name = ''
if os.path.exists('local/github-token'):
    registry_secret_name = 'skillsd-registry-github'
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
        token=encode_base64(str(read_file('local/github-token')).rstrip()),
    )))
    helm_set.append('registry.enabled=true')
    helm_set.append('registry.github.tokenSecret=' + registry_secret_name)

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
