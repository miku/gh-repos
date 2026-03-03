# gh-repos

Clone or fetch all repos from GitHub, for backup or other reasons. An afternoon
hack. Uses a GitHub [token](https://github.com/settings/tokens) stored in
`GITHUB_TOKEN` environment variable. I wanted to have this after I found a script
to [clone all gists](https://gist.github.com/miku/4502324062dc5437f98f965046920c04).

## Install

```shell
$ go install github.com/miku/gh-repos@latest
```

## Run

You can list repos and sync them.

```shell
$ gh-repos list
```

![](static/gh-repos-list-Post45-Data-Collective.png)

To sync:

```shell
$ gh-repos sync
```

## Usage

```shell
$ gh-repos -h
gh-repos - fetch and manage GitHub repositories

Usage:
  gh-repos list [-u user] [-f]                List repos by name and description
  gh-repos sync [-u user] [-d dir] [-f] [-p pattern]  Clone or pull repos

Environment:
  GITHUB_TOKEN  GitHub personal access token (required)

Subcommands:
  list    List all repositories for a user
  sync    Clone new repos and pull existing ones

Flags:
  -u string  GitHub username (default: authenticated user)
  -f         Force fresh API request, ignoring cache
  -d string  Target directory for cloned repos (sync only, default: ".")
  -p string  Filter repos by name pattern with * wildcards (sync only)
```

## Prior Art

* [How to clone all repos at once from
  GitHub?](https://stackoverflow.com/questions/19576742/how-to-clone-all-repos-at-once-from-github),
asked 12y 4m ago, 262k views
* [asottile/all-repos](https://github.com/asottile/all-repos)
* [rhysd/github-clone-all](https://github.com/rhysd/github-clone-all)
* ...
