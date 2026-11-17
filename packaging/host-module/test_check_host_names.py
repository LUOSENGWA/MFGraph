#!/usr/bin/env python3
"""Tests of check_host_names.py: the spellings of the host's name and header
prefix it must find (including those the review found missing), the plain
English and IDs it must not report, the allow marker, UTF-16/UTF-32 files,
and a run over a git repository. Run from anywhere:

  python3 packaging/host-module/test_check_host_names.py
"""
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import check_host_names as chk  # noqa: E402


def lines(text):
    return chk.scan_text(text)


class Hits(unittest.TestCase):
    def test_found(self):
        cases = {
            "joined": "deploy on AgentTeams\n",
            "singular": "class AgentTeam:\n",
            "service": "requires agentteams-service\n",
            "snake case": "agent_teams = 1\n",
            "kebab case": "agent-teams\n",
            "upper case": "AGENTTEAMS\n",
            "identifier": "func newAgentTeamsClient() {}\n",
            "proper noun": "MFGraph runs in Agent Teams.\n",
            "two spaces": "MFGraph runs in Agent  Teams.\n",
            "no-break space": "MFGraph runs in Agent\u00a0Teams.\n",
            "zero-width space": "MFGraph runs in Agent\u200bTeams.\n",
            "capitals": "AGENT TEAMS\n",
            "prefix": "X-Atf-Actor: alice\n",
            "prefix without X": "Atf-Actor: alice\n",
            "prefix in an identifier": "var XAtfActor = 1\n",
            "prefix snake case": "x_atf_actor = 1\n",
            "prefix capitals": "ATF_TOKEN=1\n",
            "prefix lower case": "x-atf-actor\n",
            "concatenation": 'h := "X-" + "Atf-" + "Actor"\n',
            "concatenation split in a word": 'h := "X-A" + "tf-Actor"\n',
            "name concatenated": 'name = "Agent" + "Teams"\n',
            "adjacent literals": 'name = "Agent" "Teams"\n',
            "list of parts": 'parts = ["Agent", "Teams"]\n',
        }
        for what, text in cases.items():
            with self.subTest(what):
                self.assertEqual(lines(text), [1], text)

    def test_across_lines(self):
        self.assertEqual(lines("one\nMFGraph runs in Agent\nTeams.\n"), [2])
        self.assertEqual(lines("// built for Agent\n// Teams\n"), [1])
        self.assertEqual(lines("# Agent\n#   Teams\n"), [1])
        self.assertEqual(lines('h := "X-" +\n\t"Atf-Actor"\n'), [2])
        self.assertEqual(lines('h := "X-A" +\n\t"tf-Actor"\n'), [1])

    def test_line_numbers(self):
        self.assertEqual(lines("a\nb\nAgentTeams\nd\nX-Atf-Team\n"), [3, 5])


class NoHits(unittest.TestCase):
    def test_plain_english_and_ids(self):
        for text in [
            "multi-agent teams coordinate their work\n",
            "an agent teams up with another one\n",
            "Multi-Agent Teams\n",
            "the AGENT team\n",
            "Agent teams are common\n",
            "Agent Teamsters\n",
            "Matfield and Batfish\n",
            "a ULID: 01HATF9K23WZC95EECHGDBSYH7\n",
            "MATFIELD\n",
            "X-Trusted-Actor\n",
        ]:
            with self.subTest(text):
                self.assertEqual(lines(text), [])

    def test_allow_marker(self):
        self.assertEqual(lines("X-Atf-Actor  # host-name-check: allow\n"), [])
        self.assertEqual(lines("Agent  // host-name-check: allow\nTeams\n"), [])
        self.assertEqual(lines("X-Atf-Actor\n# host-name-check: allow\n"), [1])


class Encodings(unittest.TestCase):
    def test_utf16_and_utf32(self):
        text = "x = 1\ny = AgentTeams\n"
        for name, data in {
            "UTF-16 LE with BOM": b"\xff\xfe" + text.encode("utf-16-le"),
            "UTF-16 BE with BOM": b"\xfe\xff" + text.encode("utf-16-be"),
            "UTF-16 LE without BOM": text.encode("utf-16-le"),
            "UTF-16 BE without BOM": text.encode("utf-16-be"),
            "UTF-32 LE with BOM": b"\xff\xfe\x00\x00" + text.encode("utf-32-le"),
            "UTF-32 BE without BOM": text.encode("utf-32-be"),
        }.items():
            with self.subTest(name):
                self.assertEqual(chk.scan("f.txt", data), ["f.txt:2"])

    def test_binary_is_skipped(self):
        self.assertEqual(chk.scan("f.bin", b"\x00\x01\x02 Atf \xff\xfe\xfd\x10\x11\x12\x13\x14\x15"), [])

    def test_path(self):
        self.assertEqual(chk.scan("docs/agentteams.md", b"clean\n"), ["docs/agentteams.md: in the path"])
        self.assertEqual(chk.scan("docs/x_atf.go", b""), ["docs/x_atf.go: in the path"])


class Repository(unittest.TestCase):
    def test_run(self):
        with tempfile.TemporaryDirectory() as root:
            def write(name, data):
                path = os.path.join(root, name)
                os.makedirs(os.path.dirname(path), exist_ok=True)
                with open(path, "wb") as f:
                    f.write(data)
            subprocess.run(["git", "init", "-q", root], check=True)
            write("clean.md", b"multi-agent teams\n")
            write("packaging/host-module/module.env", b"PREFIX=X-Atf-\n")
            write("ignored.log", b"AgentTeams\n")
            write(".gitignore", b"*.log\n")
            proc = subprocess.run([sys.executable, chk.__file__, root], capture_output=True, text=True)
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            write("src/a.go", b"package a\n\nconst h = \"X-\" + \"Atf-Actor\"\n")
            proc = subprocess.run([sys.executable, chk.__file__, root], capture_output=True, text=True)
            self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
            self.assertIn("src/a.go:3", proc.stdout)
            self.assertNotIn("module.env", proc.stdout)
            self.assertNotIn("ignored.log", proc.stdout)


if __name__ == "__main__":
    unittest.main()
