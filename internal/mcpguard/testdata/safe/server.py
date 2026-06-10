"""Safe MCP server fixture for mcpguard tests.

A benign pure function with no shell, environment, filesystem, network, or
database access. The scanner should report no critical findings here.
"""


def add(a: int, b: int) -> int:
    return a + b


def greet(name: str) -> str:
    return "hello, " + name
