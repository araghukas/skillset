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

# Optional GitHub App auth, for exercising the path the local Gitea
# stand-in can't: Gitea has no GitHub Apps, so app mode is only reachable
# against a real GitHub repo. Drop
#   local/github-app.json   (gitignored)
#
# containing:
#   {
#     "appId": "Iv23...",            // client ID or numeric app ID
#     "installationId": 12345678,
#     "privateKeyPath": "local/github-app.pem"
#   }
#
# and this Tiltfile will create a Secret from the key and switch both
# components to githubApp mode. Point skillsRepo.url / registry.* in
# local/values.yaml at the real repo the app is installed on - the app's
# credentials are useless against Gitea.

allow_k8s_contexts('kind-skillsd')

github_app = None
if os.path.exists('local/github-app.json'):
    github_app = read_json('local/github-app.json')

# Local Gitea stand-in for GitHub - see local/gitea.yaml and
# local/gitea-init.sh. Deliberately NOT managed by Tilt: Gitea's deployment
# is owned by `make gitea-up`, which applies it and then seeds it (admin
# user, repo, tokens, content) into an emptyDir. Handing the same manifest
# to Tilt would re-apply it with Tilt's own injected labels, which bumps the
# Deployment revision and - with strategy: Recreate - replaces the pod,
# discarding the emptyDir and everything gitea-init.sh just put in it. So
# Tilt only port-forwards it.
#
# The port-forward is wrapped in a retry loop because Tilt does not restart a
# serve_cmd that exits, and kubectl port-forward exits whenever the Gitea pod
# it is bound to goes away.
if not github_app:
    local_resource(
        'gitea',
        serve_cmd='while true; do kubectl --context kind-skillsd port-forward svc/gitea 3000:3000; sleep 2; done',
        readiness_probe=probe(http_get=http_get_action(port=3000, path='/api/healthz')),
        labels=['gitea'],
    )

docker_build(
    'localhost:5005/skillsd',
    '.',
    dockerfile='Dockerfile',
    ignore=['README.md', 'Makefile', '.gitignore'],
)

load("ext://base64", "encode_base64")

# Watch the credential files explicitly: os.path.exists() isn't a tracked
# read, so without these a `make gitea-up` that (re)mints tokens mid-session
# would leave Tilt holding the old secrets - or none at all.
watch_file('local/git-skillsd-token')
watch_file('local/git-skillsd-registry-token')
watch_file('local/github-app.json')

# GitHub App auth, if configured. Takes precedence over the token files
# below: the chart rejects having both set on the same component, and an app
# is the more deliberate choice of the two to have made.

if github_app:
    watch_file(github_app['privateKeyPath'])

    k8s_yaml(blob('''
apiVersion: v1
kind: Secret
metadata:
  name: skillsd-github-app
type: Opaque
data:
  private-key.pem: {key}
'''.format(key=encode_base64(str(read_file(github_app['privateKeyPath']))))))

    print('Tiltfile: using GitHub App auth (app %s, installation %s)' % (
        github_app['appId'], github_app['installationId']))

# Read-only repo auth: create a Secret from local/git-skillsd-token, if present

git_secret_name = ''
if not github_app and os.path.exists('local/git-skillsd-token'):
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

registry_enabled = False
registry_secret_name = ''
if not github_app and os.path.exists('local/git-skillsd-registry-token'):
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
    registry_enabled = True
    helm_set.append('registry.enabled=true')
    helm_set.append('registry.github.tokenSecret=' + registry_secret_name)

# In app mode both components share the one installation - a single app
# installed on the target repo is what you'd actually have locally, and
# splitting read from write is a production concern (see charts/skillsd's
# values.yaml), not something the local loop needs to model.

if github_app:
    registry_enabled = True
    helm_set.append('registry.enabled=true')
    for prefix in ['skillsRepo.githubApp', 'registry.github.githubApp']:
        helm_set.append('%s.appId=%s' % (prefix, github_app['appId']))
        helm_set.append('%s.installationId=%s' % (prefix, github_app['installationId']))
        helm_set.append('%s.privateKeySecret=skillsd-github-app' % prefix)

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

if registry_enabled:
    k8s_resource(
        'skillsd-registry',
        port_forwards=['8081:8081'],
    )
