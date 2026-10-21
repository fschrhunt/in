"""Pin failure cases that would otherwise allow broken docs or release selection."""

from pathlib import Path
import tempfile
import unittest

from check_docs import check
from release import release_notes


class ReleaseNotesTests(unittest.TestCase):
    """Release notes must identify exactly one stable version section."""

    def test_extracts_only_selected_section(self):
        notes = '# Changelog\n\n## Unreleased\n\nLater\n\n## 1.2.3\n\n### Fixes\n\n- Fixed.\n\n## 1.2.2\n\nEarlier\n'
        self.assertEqual(release_notes('v1.2.3', notes), '### Fixes\n\n- Fixed.\n')

    def test_rejects_unsafe_or_ambiguous_tags(self):
        for tag in ('../escape', 'v01.2.3', '1.2.3', 'v1.2.3-rc1', 'v1.2.3\n'):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release_notes(tag, '## 1.2.3\nNotes\n')

    def test_requires_release_notes(self):
        with self.assertRaises(ValueError):
            release_notes('v1.2.3', '## Unreleased\nPending\n')


class DocsTests(unittest.TestCase):
    """Local links and guide navigation must fail when their targets drift."""

    def test_rejects_missing_heading(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'README.md').write_text('# Start\n\n[broken](#missing)\n')
            self.assertTrue(any('missing heading' in error for error in check(root)))

    def test_rejects_duplicate_page_order(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            group = root / 'docs/guides/start'
            group.mkdir(parents=True)
            for name in ('README', 'one', 'two'):
                (group / f'{name}.md').write_text(f'---\ntitle: {name}\ndescription: Test\norder: 1\n---\n\n# {name}\n')
            self.assertTrue(any('order duplicates' in error for error in check(root)))
