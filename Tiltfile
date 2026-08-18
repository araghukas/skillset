# Local dev loop for skillsd.

# Prerequisites (one-time):
#   `ctlptl apply -f local/cluster.yaml`

# Then:
#   `tilt up`

# OR, just run the following to start the cluster AND do Tilt up:
#   `made dev`

# Optional read-only repo auth: drop a fine-grained GitHub token
# ("Contents: read") for skillsRepo.url at local/git-skillsd-token
# (gitignored) to have it wired in as skillsRepo.tokenSecret.

# Optional registry write auth: drop a token with push + PR write access for
# registry.skillsRepo.url at local/git-skillsd-registry-token (gitignored)
# to enable the registry and wire it in as registry.github.tokenSecret.

# Optional GitHub App auth (only reachable against a real GitHub repo -
# Gitea has no GitHub Apps). Drop local/github-app.json (gitignored):
#   {
#     "appId": "Iv23...",
#     "installationId": 12345678,
#     "privateKeyPath": "local/github-app.pem",
#     "owner": "your-org",   // optional, overrides skillsRepo.url etc.
#     "repo": "your-repo",   // optional
#     "branch": "main"       // optional, defaults to "main"
#   }
# Switches both components to githubApp mode; owner/repo (if set) point
# them at https://github.com/<owner>/<repo>.git without editing values.yaml.

allow_k8s_contexts('kind-skillsd')

github_app = None
if os.path.exists('local/github-app.json'):
    github_app = read_json('local/github-app.json')

# Gitea stand-in for GitHub, deliberately NOT managed by Tilt: it's owned by
# `make gitea-up`, which seeds it into an emptyDir that Tilt re-applying the
# manifest would wipe. Tilt only port-forwards it (retry loop because Tilt
# won't restart an exited serve_cmd, and the forward dies with the pod).
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
    # Only these paths feed the built binaries (Dockerfile COPYs everything,
    # but go build only touches these) - restricting to them, rather than
    # denylisting non-build files like docs/ and scripts/, means new
    # non-build files never need to be added here to avoid spurious rebuilds.
    only=['go.mod', 'go.sum', 'cmd', 'internal'],
)

load("ext://base64", "encode_base64")

# os.path.exists() isn't a tracked read, so watch these explicitly.
watch_file('local/git-skillsd-token')
watch_file('local/git-skillsd-registry-token')
watch_file('local/github-app.json')

# GitHub App auth takes precedence over the token files below - the chart
# rejects having both set on the same component.

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

# App mode: both components share the one installation.
if github_app:
    registry_enabled = True
    helm_set.append('registry.enabled=true')
    for prefix in ['skillsRepo.githubApp', 'registry.github.githubApp']:
        helm_set.append('%s.appId=%s' % (prefix, github_app['appId']))
        helm_set.append('%s.installationId=%s' % (prefix, github_app['installationId']))
        helm_set.append('%s.privateKeySecret=skillsd-github-app' % prefix)

    owner = github_app.get('owner', '')
    repo = github_app.get('repo', '')
    if owner and repo:
        repo_url = 'https://github.com/%s/%s.git' % (owner, repo)
        branch = github_app.get('branch', 'main')
        helm_set.append('skillsRepo.url=' + repo_url)
        helm_set.append('skillsRepo.branch=' + branch)
        helm_set.append('registry.skillsRepo.url=' + repo_url)
        helm_set.append('registry.skillsRepo.baseBranch=' + branch)
        helm_set.append('registry.github.owner=' + owner)
        helm_set.append('registry.github.repo=' + repo)
        helm_set.append('registry.github.apiBaseURL=https://api.github.com')

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
