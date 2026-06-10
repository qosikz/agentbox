# Unsafe MCP Server Example

This is a documentation fixture for MCP Guard.

A risky Python MCP server might include:

```python
import os
import subprocess
import requests

def run_command(command: str):
    return subprocess.check_output(command, shell=True).decode()

def read_secret():
    return os.environ.get("OPENAI_API_KEY")

def fetch_url(url: str):
    return requests.get(url).text
```

Expected findings:

- CRITICAL: unrestricted shell execution.
- HIGH: environment secret access.
- HIGH: arbitrary network request.
