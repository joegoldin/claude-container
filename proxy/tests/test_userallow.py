"""Tests for the user_allow nft template compiler."""

import pytest
from claude_proxy.userallow import compile_template, TEMPLATES


def test_outbound_icmp_echo():
    stmt = compile_template("outbound_icmp_echo", {"addr": "8.8.8.8"})
    assert stmt == "ip daddr 8.8.8.8 icmp type echo-request accept"


def test_inbound_icmp_echo():
    stmt = compile_template("inbound_icmp_echo", {"addr": "192.168.1.0/24"})
    assert stmt == "ip saddr 192.168.1.0/24 icmp type echo-reply accept"


def test_outbound_tcp_cidr():
    stmt = compile_template("outbound_tcp_cidr",
                            {"cidr": "10.0.0.0/8", "port": 22})
    assert stmt == "ip daddr 10.0.0.0/8 tcp dport 22 accept"


def test_inbound_tcp_cidr():
    stmt = compile_template("inbound_tcp_cidr",
                            {"cidr": "192.168.1.0/24", "port": 3000})
    assert stmt == "ip saddr 192.168.1.0/24 tcp dport 3000 accept"


def test_outbound_udp_cidr():
    stmt = compile_template("outbound_udp_cidr",
                            {"cidr": "8.8.8.8", "port": 53})
    assert stmt == "ip daddr 8.8.8.8 udp dport 53 accept"


def test_outbound_any_protocol():
    stmt = compile_template("outbound_any", {"addr": "1.1.1.1"})
    assert stmt == "ip daddr 1.1.1.1 accept"


def test_unknown_template_raises():
    with pytest.raises(ValueError, match="unknown template"):
        compile_template("delete_chain", {})


def test_missing_param_raises():
    with pytest.raises(ValueError, match="missing"):
        compile_template("outbound_tcp_cidr", {"cidr": "10.0.0.0/8"})


def test_addr_validation_rejects_garbage():
    with pytest.raises(ValueError, match="invalid"):
        compile_template("outbound_any", {"addr": "; drop chain;"})


def test_port_validation_rejects_garbage():
    with pytest.raises(ValueError, match="port"):
        compile_template("outbound_tcp_cidr",
                         {"cidr": "10.0.0.0/8", "port": "22; drop chain"})


def test_port_out_of_range():
    with pytest.raises(ValueError, match="port"):
        compile_template("outbound_tcp_cidr",
                         {"cidr": "10.0.0.0/8", "port": 99999})


def test_templates_metadata_lists_all():
    """TEMPLATES dict exposes all template names with their fields."""
    names = set(TEMPLATES.keys())
    assert names == {
        "outbound_icmp_echo", "inbound_icmp_echo",
        "outbound_tcp_cidr", "inbound_tcp_cidr", "outbound_udp_cidr",
        "outbound_any",
    }
