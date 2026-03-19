#!/usr/bin/env python3
"""
update_readme.py — rewrites dynamic zones in README.md using GitHub API data.

Dynamic zones are delimited by HTML comment markers:
  <!-- ZONE_START --> ... <!-- ZONE_END -->

Zones updated:
  - PROJECTS: top public repos ranked by stars + recency (no forks, no profile repo)
  - OSS:      recent external PRs from public events
  - STATS:    github-readme-stats image (static URL, auto-refreshes on render)

Requires:
  - GH_TOKEN env var (PAT with repo + read:user scope)
"""

import os
import re
import sys
from datetime import datetime, timezone
from urllib.request import Request, urlopen
from urllib.error import URLError
import json

GITHUB_USERNAME = "jeziellopes"
README_PATH = os.path.join(os.path.dirname(__file__), "..", "README.md")
API_BASE = "https://api.github.com"

TOP_PROJECTS_COUNT = 4
OSS_CONTRIBUTIONS_COUNT = 5


def gh_get(path: str, token: str) -> list | dict:
    url = f"{API_BASE}{path}"
    req = Request(url, headers={
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    })
    with urlopen(req) as resp:
        return json.loads(resp.read())


def score_repo(repo: dict) -> float:
    """Rank repos by stars (primary) and recency (secondary)."""
    stars = repo.get("stargazers_count", 0)
    updated = repo.get("updated_at", "1970-01-01T00:00:00Z")
    days_ago = (datetime.now(timezone.utc) - datetime.fromisoformat(updated.replace("Z", "+00:00"))).days
    recency_score = max(0, 365 - days_ago) / 365  # 0–1 scale
    return stars * 10 + recency_score


def build_projects_section(token: str) -> str:
    repos = gh_get(f"/users/{GITHUB_USERNAME}/repos?sort=updated&per_page=100&type=owner", token)
    public = [
        r for r in repos
        if not r["private"]
        and not r["fork"]
        and r["name"] != GITHUB_USERNAME
    ]
    top = sorted(public, key=score_repo, reverse=True)[:TOP_PROJECTS_COUNT]

    rows = []
    for r in top:
        name = r["name"]
        url = r["html_url"]
        desc = (r.get("description") or "").replace("|", "\\|")
        stars = r.get("stargazers_count", 0)
        forks = r.get("forks_count", 0)
        rows.append(f"| [{name}]({url}) | {desc} | ⭐ {stars} | 🍴 {forks} |")

    lines = [
        "## 🚀 Featured Projects",
        "",
        "| Project | Description | Stars | Forks |",
        "|---------|-------------|-------|-------|",
        *rows,
    ]
    return "\n".join(lines)


def build_oss_section(token: str) -> str:
    events = gh_get(f"/users/{GITHUB_USERNAME}/events?per_page=100", token)
    contributions = []
    seen = set()

    for event in events:
        if event.get("type") != "PullRequestEvent":
            continue
        repo_name = event.get("repo", {}).get("name", "")
        owner = repo_name.split("/")[0] if "/" in repo_name else ""
        if owner == GITHUB_USERNAME:
            continue  # skip own repos

        pr_payload = event.get("payload", {}).get("pull_request", {})
        pr_api_url = pr_payload.get("url", "")
        pr_number = pr_payload.get("number")

        if not pr_api_url or pr_api_url in seen:
            continue
        seen.add(pr_api_url)

        # The events API returns a minimal PR object — fetch the full details
        try:
            pr = gh_get(pr_api_url.replace(API_BASE, ""), token)
        except Exception:
            continue

        pr_url = pr.get("html_url", f"https://github.com/{repo_name}/pull/{pr_number}")
        pr_title = pr.get("title") or repo_name
        merged = pr.get("merged_at")
        pr_state = pr.get("state", "open")

        if merged:
            status = "✅ Merged"
        elif pr_state == "open":
            status = "🔄 Open"
        else:
            status = "❌ Closed"

        repo_url = f"https://github.com/{repo_name}"
        contributions.append((pr_title, pr_url, repo_name, repo_url, status))
        if len(contributions) >= OSS_CONTRIBUTIONS_COUNT:
            break

    if not contributions:
        return "## 🤝 OSS Contributions\n\n_No recent external contributions found._"

    rows = [
        f"| [{title}]({pr_url}) | [{repo}]({repo_url}) | {status} |"
        for title, pr_url, repo, repo_url, status in contributions
    ]
    lines = [
        "## 🤝 OSS Contributions",
        "",
        "| PR | Repository | Status |",
        "|----|-----------|--------|",
        *rows,
    ]
    return "\n".join(lines)


def build_stats_section() -> str:
    url = (
        f"https://github-readme-stats.vercel.app/api"
        f"?username={GITHUB_USERNAME}"
        f"&show_icons=true"
        f"&count_private=true"
        f"&hide_border=true"
        f"&theme=dark"
    )
    return (
        "## 📊 GitHub Stats\n\n"
        f'<p>\n  <img src="{url}" />\n</p>'
    )


def rewrite_zone(content: str, zone: str, new_body: str) -> str:
    pattern = re.compile(
        rf"(<!-- {zone}_START -->)\n.*?\n(<!-- {zone}_END -->)",
        re.DOTALL,
    )
    replacement = rf"\1\n{new_body}\n\2"
    updated, count = pattern.subn(replacement, content)
    if count == 0:
        print(f"WARNING: zone {zone} not found in README", file=sys.stderr)
    return updated


def main() -> None:
    token = os.environ.get("GH_TOKEN", "")
    if not token:
        print("ERROR: GH_TOKEN environment variable is not set.", file=sys.stderr)
        sys.exit(1)

    readme_path = os.path.abspath(README_PATH)
    with open(readme_path, encoding="utf-8") as f:
        original = f.read()

    try:
        projects = build_projects_section(token)
        oss = build_oss_section(token)
        stats = build_stats_section()
    except URLError as e:
        print(f"ERROR: GitHub API request failed: {e}", file=sys.stderr)
        sys.exit(1)

    updated = original
    updated = rewrite_zone(updated, "PROJECTS", projects)
    updated = rewrite_zone(updated, "OSS", oss)
    updated = rewrite_zone(updated, "STATS", stats)

    if updated == original:
        print("README.md is already up to date.")
        return

    with open(readme_path, "w", encoding="utf-8") as f:
        f.write(updated)
    print("README.md updated.")


if __name__ == "__main__":
    main()
