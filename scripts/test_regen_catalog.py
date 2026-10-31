"""Offline regression tests for the public catalog refresh."""
import datetime
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("regen_catalog", Path(__file__).with_name("regen-catalog.py"))
catalog = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(catalog)


class Response:
    def __init__(self, stdout="", returncode=0):
        self.stdout, self.returncode = stdout, returncode


class CatalogRefreshTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.output = Path(self.tmp.name) / "catalog.go"
        self.original = ('package catalog\n\n'
                         'var generatedBootcImages = []BootcImage{\n'
                         '\t{"aurora", "old", "ghcr.io/ublue-os/aurora:latest", "source", "kde"},\n'
                         '\t{"retained", "old", "ghcr.io/tuna-os/retained:old", "source", "linux"},\n'
                         '}\n\nvar generatedISOs = []Image{\n'
                         '\t{Name: "existing-iso", Description: "old", ISO: "https://example.com/old.iso", Source: "source", Logo: "linux", Variant: "desktop"},\n'
                         '}\n')
        self.output.write_text(self.original)
        self.date = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
        self.digest = "sha256:" + "a" * 64
        patches = [patch.object(catalog, "OUT", str(self.output)),
                   patch.object(catalog, "META", {"ublue-os/bluefin": ("Bluefin", "gnome", "gnome", "stable")}),
                   patch.object(catalog, "ISO_CANDIDATES", [("new-iso", "https://example.com/new.iso", "new", "source", "linux")])]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def token(self, repo):
        return None if repo == "tuna-os/retained" else "public-token"

    def release(self, repo, tag, token):
        return (self.digest, self.date) if tag in ("stable", "latest") else None

    def iso(self, *args):
        return Response("HTTP/2 200\r\nContent-Type: application/vnd.efi.iso\r\nLast-Modified: " +
                        self.date.strftime("%a, %d %b %Y %H:%M:%S GMT") + "\r\n\r\n")

    def test_refresh_preserves_missing_entries_and_round_trips(self):
        with patch.object(catalog, "registry_token", side_effect=self.token), \
             patch.object(catalog, "image_release", side_effect=self.release), \
             patch.object(catalog, "sh", side_effect=self.iso):
            catalog.main()
            first = self.output.read_text()
            self.assertIn('"ghcr.io/ublue-os/bluefin:stable"', first)
            self.assertIn('"retained"', first)
            self.assertIn('"existing-iso"', first)
            self.assertIn('// upstream: ' + self.digest, first)
            catalog.main()
            self.assertEqual(self.output.read_text(), first)

    def test_network_failure_never_replaces_catalog(self):
        with patch.object(catalog, "registry_token", side_effect=catalog.RefreshError("HTTP 429")):
            with self.assertRaises(catalog.RefreshError):
                catalog.main()
        self.assertEqual(self.output.read_text(), self.original)

    def test_completely_unavailable_registry_fails_without_writing(self):
        with patch.object(catalog, "registry_token", return_value=None):
            with self.assertRaises(catalog.RefreshError):
                catalog.main()
        self.assertEqual(self.output.read_text(), self.original)

    def test_only_denied_token_is_treated_as_missing(self):
        def error(body):
            return urllib.error.HTTPError("https://ghcr.io/token", 403, "Forbidden", {}, io.BytesIO(body))
        with patch.object(catalog.OPENER, "open", side_effect=error(b'{"errors":[{"code":"DENIED"}]}')):
            self.assertIsNone(catalog.registry_token("ublue-os/missing"))
        with patch.object(catalog.OPENER, "open", side_effect=error(b'{"errors":[{"code":"TOOMANYREQUESTS"}]}')):
            with self.assertRaises(catalog.RefreshError):
                catalog.registry_token("ublue-os/aurora")

    def test_cross_host_redirect_drops_authorization(self):
        request = urllib.request.Request("https://ghcr.io/v2/example", headers={"Authorization": "Bearer secret"})
        redirect = catalog.SafeRedirect().redirect_request(request, None, 302, "Found", {}, "https://example.com/object")
        self.assertIsNone(redirect.get_header("Authorization"))

    def test_iso_error_after_successful_images_never_replaces_catalog(self):
        with patch.object(catalog, "registry_token", side_effect=self.token), \
             patch.object(catalog, "image_release", side_effect=self.release), \
             patch.object(catalog, "sh", return_value=Response(returncode=22)):
            with self.assertRaises(catalog.RefreshError):
                catalog.main()
        self.assertEqual(self.output.read_text(), self.original)

    def test_non_success_or_missing_last_modified_is_an_error(self):
        for headers in ("HTTP/2 404\r\nLast-Modified: Thu, 01 Jan 2026 00:00:00 GMT\r\n\r\n",
                        "HTTP/2 200\r\nContent-Type: text/html\r\n\r\n",
                        "HTTP/2 200\r\nContent-Type: application/vnd.efi.iso\r\n\r\n"):
            with patch.object(catalog, "sh", return_value=Response(headers)):
                with self.assertRaises(catalog.RefreshError):
                    catalog.iso_last_modified("https://example.com/image.iso")

    def test_redirect_uses_final_headers(self):
        headers = ("HTTP/2 302\r\nLast-Modified: Thu, 01 Jan 2026 00:00:00 GMT\r\n\r\n"
                   "HTTP/2 200\r\nContent-Type: application/vnd.efi.iso\r\nLast-Modified: Fri, 02 Jan 2026 00:00:00 GMT\r\n\r\n")
        with patch.object(catalog, "sh", return_value=Response(headers)):
            self.assertEqual(catalog.iso_last_modified("https://example.com/image.iso").day, 2)

    def test_existing_tag_not_replaced_by_another_flavour(self):
        existing, _ = catalog.existing_catalog()
        with patch.object(catalog, "registry_token", return_value="public-token"), \
             patch.object(catalog, "image_release", return_value=None):
            self.assertEqual(catalog.fetch_images(existing), existing)

    def test_stale_isos_preserve_existing(self):
        old = {"old": ("old", "description", "https://example.com/old.iso", "source", "logo", "")}
        with patch.object(catalog, "iso_last_modified", return_value=self.date - datetime.timedelta(days=90)):
            self.assertEqual(catalog.fetch_isos(old), old)

    def test_catalog_name_collision_fails(self):
        existing, _ = catalog.existing_catalog()
        existing["bluefin"] = ("bluefin", "other", "ghcr.io/tuna-os/other:latest", "source", "logo", "")
        with patch.object(catalog, "registry_token", side_effect=self.token), \
             patch.object(catalog, "image_release", side_effect=self.release):
            with self.assertRaises(catalog.RefreshError):
                catalog.fetch_images(existing)

    def test_unparseable_existing_catalog_is_not_overwritten(self):
        self.output.write_text(self.original.replace('"old", "ghcr.io', 'unknown, "ghcr.io', 1))
        invalid = self.output.read_text()
        with self.assertRaises(catalog.RefreshError):
            catalog.main()
        self.assertEqual(self.output.read_text(), invalid)

    def test_manifest_digest_and_architecture_are_checked(self):
        def encoded(data):
            body = json.dumps(data).encode()
            return body, "sha256:" + hashlib.sha256(body).hexdigest()
        config, config_digest = encoded({"os": "linux", "architecture": "amd64", "created": self.date.isoformat()})
        manifest, digest = encoded({"config": {"digest": config_digest}})
        responses = [(json.loads(manifest), {"Docker-Content-Digest": digest}, manifest),
                     (json.loads(config), {}, config)]
        with patch.object(catalog, "registry_json", side_effect=responses):
            self.assertEqual(catalog.image_release("ublue-os/aurora", "latest", "public-token"), (digest, self.date))
        responses[0] = (json.loads(manifest), {"Docker-Content-Digest": self.digest}, manifest)
        with patch.object(catalog, "registry_json", side_effect=responses):
            with self.assertRaises(catalog.RefreshError):
                catalog.image_release("ublue-os/aurora", "latest", "public-token")

    def test_multiarch_index_validates_child_and_config(self):
        def encoded(data):
            body = json.dumps(data).encode()
            return body, "sha256:" + hashlib.sha256(body).hexdigest()
        config, config_digest = encoded({"os": "linux", "architecture": "amd64", "created": self.date.isoformat()})
        child, child_digest = encoded({"config": {"digest": config_digest}})
        index, digest = encoded({"manifests": [{"platform": {"os": "linux", "architecture": "amd64"}, "digest": child_digest}]})
        responses = [(json.loads(index), {"Docker-Content-Digest": digest}, index),
                     (json.loads(child), {}, child), (json.loads(config), {}, config)]
        with patch.object(catalog, "registry_json", side_effect=responses):
            self.assertEqual(catalog.image_release("tuna-os/example", "latest", "public-token"), (digest, self.date))
        responses[1] = (json.loads(child), {}, child + b' ')
        with patch.object(catalog, "registry_json", side_effect=responses):
            with self.assertRaises(catalog.RefreshError):
                catalog.image_release("tuna-os/example", "latest", "public-token")


if __name__ == "__main__":
    unittest.main()
