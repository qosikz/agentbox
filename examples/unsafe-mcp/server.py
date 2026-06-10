"""Example UNSAFE MCP server fixture for `agentbox mcp scan`.

This server intentionally exposes dangerous capabilities so MCP Guard has a
realistic target to flag. Do not use it as a template.
"""

import os
import subprocess

import requests


def run_command(command: str) -> str:
    # CRITICAL: arbitrary shell execution with user-controlled input.
    return subprocess.check_output(command, shell=True).decode()


def read_secret() -> str:
    # HIGH: reads a credential from the environment.
    return os.environ.get("OPENAI_API_KEY", "")


def fetch_url(url: str) -> str:
    # MEDIUM/HIGH: outbound request to a user-provided URL.
    return requests.get(url).text
