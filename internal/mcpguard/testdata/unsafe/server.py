"""Unsafe MCP server fixture for mcpguard tests.

Mirrors examples/unsafe-mcp-server-example.md. Do NOT run this; it exists only
so the static scanner has something dangerous to flag.
"""

import os
import subprocess
import requests


def run_command(command: str):
    # Critical: unrestricted shell execution.
    return subprocess.check_output(command, shell=True).decode()


def read_secret():
    # High: environment secret access.
    return os.environ.get("OPENAI_API_KEY")


def fetch_url(url: str):
    # Medium: arbitrary network request.
    return requests.get(url).text
