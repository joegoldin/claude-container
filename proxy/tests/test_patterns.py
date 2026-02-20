"""Tests for the pattern generator functions."""

import re

from claude_proxy.patterns import base_domain, exact_url, subdomain_pattern, url_no_params


class TestExactUrl:
    """Tests for exact_url pattern generator."""

    def test_matches_exact(self):
        """Exact URL matches the generated pattern."""
        url = "https://api.github.com/repos?page=1"
        pattern = exact_url(url)
        assert re.match(pattern, url), f"Pattern {pattern!r} should match {url!r}"

    def test_rejects_different_params(self):
        """A URL with different query params does not match."""
        url = "https://api.github.com/repos?page=1"
        pattern = exact_url(url)
        different = "https://api.github.com/repos?page=2"
        assert not re.match(pattern, different), (
            f"Pattern {pattern!r} should NOT match {different!r}"
        )

    def test_escapes_special_chars(self):
        """Dots, question marks, and ampersands are properly escaped."""
        url = "https://example.com/search?q=a&b=c"
        pattern = exact_url(url)
        # The exact URL must match
        assert re.match(pattern, url)
        # A URL where the dot is replaced by an arbitrary char must NOT match
        mangled = "https://exampleXcom/search?q=a&b=c"
        assert not re.match(pattern, mangled), (
            f"Pattern {pattern!r} should NOT match {mangled!r} (dot not escaped)"
        )


class TestUrlNoParams:
    """Tests for url_no_params pattern generator."""

    def test_matches_without_params(self):
        """Bare URL (no query string) matches."""
        url = "https://api.github.com/repos"
        pattern = url_no_params(url)
        assert re.match(pattern, url), f"Pattern {pattern!r} should match {url!r}"

    def test_matches_with_any_params(self):
        """URL with any query string matches."""
        url = "https://api.github.com/repos"
        pattern = url_no_params(url)
        with_params = "https://api.github.com/repos?page=5&per_page=100"
        assert re.match(pattern, with_params), (
            f"Pattern {pattern!r} should match {with_params!r}"
        )

    def test_rejects_different_path(self):
        """A URL with a different path does not match."""
        url = "https://api.github.com/repos"
        pattern = url_no_params(url)
        different = "https://api.github.com/users"
        assert not re.match(pattern, different), (
            f"Pattern {pattern!r} should NOT match {different!r}"
        )

    def test_strips_existing_params(self):
        """If the input URL already has query params, they are stripped."""
        url_with_params = "https://api.github.com/repos?page=1"
        pattern = url_no_params(url_with_params)
        # Should match the bare URL
        assert re.match(pattern, "https://api.github.com/repos")
        # Should match with different params
        assert re.match(pattern, "https://api.github.com/repos?foo=bar")


class TestSubdomainPattern:
    """Tests for subdomain_pattern pattern generator."""

    def test_matches_subdomain(self):
        """The specified host matches on https."""
        host = "api.github.com"
        pattern = subdomain_pattern(host)
        url = "https://api.github.com/some/path"
        assert re.match(pattern, url), f"Pattern {pattern!r} should match {url!r}"

    def test_rejects_different_subdomain(self):
        """A different subdomain does not match."""
        host = "api.github.com"
        pattern = subdomain_pattern(host)
        different = "https://cdn.github.com/asset"
        assert not re.match(pattern, different), (
            f"Pattern {pattern!r} should NOT match {different!r}"
        )

    def test_matches_http_and_https(self):
        """Both http and https schemes match."""
        host = "api.github.com"
        pattern = subdomain_pattern(host)
        assert re.match(pattern, "https://api.github.com/path")
        assert re.match(pattern, "http://api.github.com/path")


class TestBaseDomain:
    """Tests for base_domain pattern generator."""

    def test_matches_bare_domain(self):
        """The bare domain (no subdomain) matches."""
        domain = "github.com"
        pattern = base_domain(domain)
        url = "https://github.com/page"
        assert re.match(pattern, url), f"Pattern {pattern!r} should match {url!r}"

    def test_matches_any_subdomain(self):
        """Any subdomain of the domain matches."""
        domain = "github.com"
        pattern = base_domain(domain)
        assert re.match(pattern, "https://api.github.com/repos")
        assert re.match(pattern, "https://cdn.github.com/asset")
        assert re.match(pattern, "http://deep.nested.github.com/x")

    def test_rejects_different_domain(self):
        """A completely different domain does not match."""
        domain = "github.com"
        pattern = base_domain(domain)
        different = "https://gitlab.com/page"
        assert not re.match(pattern, different), (
            f"Pattern {pattern!r} should NOT match {different!r}"
        )

    def test_does_not_match_suffix(self):
        """A domain that merely ends with the pattern domain must NOT match.

        e.g. "hub.com" pattern must NOT match "github.com".
        """
        domain = "hub.com"
        pattern = base_domain(domain)
        assert not re.match(pattern, "https://github.com/page"), (
            f"Pattern {pattern!r} should NOT match github.com (suffix trap)"
        )
        # But it should still match the actual domain and its subdomains
        assert re.match(pattern, "https://hub.com/page")
        assert re.match(pattern, "https://api.hub.com/page")
