"""Regex pattern generators for the proxy rule engine.

Converts user-friendly URL/domain selections into regex strings
suitable for the RuleStore. Used by the mitmproxy addon and the
web dashboard frontend.
"""

import re
import urllib.parse


def exact_url(url: str) -> str:
    """Match this exact URL and nothing else.

    Special regex characters in the URL are escaped so the pattern
    is a literal match.

    Example:
        >>> exact_url("https://api.github.com/repos?page=1")
        '^https://api\\.github\\.com/repos\\?page=1$'
    """
    return f"^{re.escape(url)}$"


def url_no_params(url: str) -> str:
    """Match this URL with or without query parameters.

    Any existing query string in the input URL is stripped before
    building the pattern.

    Example:
        >>> url_no_params("https://api.github.com/repos")
        '^https://api\\.github\\.com/repos(\\\\?.*)?$'
    """
    parsed = urllib.parse.urlparse(url)
    # Rebuild without query string and fragment
    bare = urllib.parse.urlunparse((
        parsed.scheme,
        parsed.netloc,
        parsed.path,
        "",  # params
        "",  # query
        "",  # fragment
    ))
    return rf"^{re.escape(bare)}(\?.*)?$"


def subdomain_pattern(host: str) -> str:
    """Match this exact host on http or https, any path.

    Example:
        >>> subdomain_pattern("api.github.com")
        '^https?://api\\.github\\.com(/.*)?$'
    """
    return rf"^https?://{re.escape(host)}(/.*)?$"


def base_domain(domain: str) -> str:
    """Match this domain and ALL subdomains on http or https.

    The pattern requires that if a subdomain is present it must be
    separated by a dot, preventing suffix matches (e.g. "hub.com"
    will not match "github.com").

    Example:
        >>> base_domain("github.com")
        '^https?://([^/]*\\.)?github\\.com(/.*)?$'
    """
    return rf"^https?://([^/]*\.)?{re.escape(domain)}(/.*)?$"
