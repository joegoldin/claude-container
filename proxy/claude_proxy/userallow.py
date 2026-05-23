"""Compile user-friendly firewall templates into nft statement strings.

The compiler is the only place user input enters the nft pipeline. It
validates each parameter against a strict regex and emits a canonical
statement string that `publish-mgr` then re-validates (keyword
blacklist + `nft --check`) before committing.
"""

import re

# Address-or-CIDR pattern. Accepts IPv4 like "192.168.1.1" or
# "10.0.0.0/8". Rejects anything containing whitespace, shell
# metacharacters, or nft keywords.
_ADDR_RE = re.compile(r"^\d{1,3}(\.\d{1,3}){3}(/\d{1,2})?$")


def _check_addr(value: str) -> str:
    if not isinstance(value, str) or not _ADDR_RE.match(value):
        raise ValueError(f"invalid address {value!r}")
    return value


def _check_port(value) -> int:
    """Coerce to int and range-check 1..65535."""
    try:
        port = int(value)
    except (TypeError, ValueError):
        raise ValueError(f"port {value!r} is not an integer")
    if port < 1 or port > 65535:
        raise ValueError(f"port {port} out of range 1-65535")
    return port


def _outbound_icmp_echo(params: dict) -> str:
    addr = _check_addr(_require(params, "addr"))
    return f"ip daddr {addr} icmp type echo-request accept"


def _inbound_icmp_echo(params: dict) -> str:
    addr = _check_addr(_require(params, "addr"))
    return f"ip saddr {addr} icmp type echo-reply accept"


def _outbound_tcp_cidr(params: dict) -> str:
    cidr = _check_addr(_require(params, "cidr"))
    port = _check_port(_require(params, "port"))
    return f"ip daddr {cidr} tcp dport {port} accept"


def _inbound_tcp_cidr(params: dict) -> str:
    cidr = _check_addr(_require(params, "cidr"))
    port = _check_port(_require(params, "port"))
    return f"ip saddr {cidr} tcp dport {port} accept"


def _outbound_udp_cidr(params: dict) -> str:
    cidr = _check_addr(_require(params, "cidr"))
    port = _check_port(_require(params, "port"))
    return f"ip daddr {cidr} udp dport {port} accept"


def _outbound_any(params: dict) -> str:
    addr = _check_addr(_require(params, "addr"))
    return f"ip daddr {addr} accept"


def _require(params: dict, key: str):
    if key not in params:
        raise ValueError(f"missing parameter {key!r}")
    return params[key]


# Public registry — each entry maps template_name → (compile_fn, [field_names]).
# The dashboard UI uses the field list to build the form.
TEMPLATES = {
    "outbound_icmp_echo": (_outbound_icmp_echo, ["addr"]),
    "inbound_icmp_echo":  (_inbound_icmp_echo,  ["addr"]),
    "outbound_tcp_cidr":  (_outbound_tcp_cidr,  ["cidr", "port"]),
    "inbound_tcp_cidr":   (_inbound_tcp_cidr,   ["cidr", "port"]),
    "outbound_udp_cidr":  (_outbound_udp_cidr,  ["cidr", "port"]),
    "outbound_any":       (_outbound_any,       ["addr"]),
}


def compile_template(name: str, params: dict) -> str:
    """Compile one of the well-known templates into an nft statement.

    Raises ValueError on unknown template or invalid params.
    """
    entry = TEMPLATES.get(name)
    if entry is None:
        raise ValueError(f"unknown template {name!r}")
    fn, _fields = entry
    return fn(params)
