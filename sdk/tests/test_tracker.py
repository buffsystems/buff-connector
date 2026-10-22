"""Tests for the shared tracker layer (#288).

Written against what a tracker plugin actually receives: Jira's wiki mention,
GitHub's slash command, a comment where the instruction sits before the mention,
a bot addressed by a display name it does not know it has.
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from buff_plugin import (  # noqa: E402
    Command, Identity, Scope, after, at_second, event_id, moment, parse_command, source,
)


BOT = Identity(name="buff", key="USER10100", display="Buff", alias="buff-bot",
               mentions=("[~buff]", "[~USER10100]"))


class TestIdentity(unittest.TestCase):
    def test_answers_to_any_of_its_names(self):
        # Jira's changelog carries an internal key where the API carries a login,
        # and a display name where it carries neither (#156).
        self.assertTrue(BOT.is_me("buff"))
        self.assertTrue(BOT.is_me("USER10100"))
        self.assertTrue(BOT.is_me("Buff"))
        self.assertTrue(BOT.is_me("BUFF"))

    def test_not_someone_else(self):
        self.assertFalse(BOT.is_me("buffalo"))
        self.assertFalse(BOT.is_me(""))
        self.assertFalse(BOT.is_me(None))


class TestParseCommand(unittest.TestCase):
    def test_mention_without_command_is_ask(self):
        cmd = parse_command("@buff почему падает экспорт?", BOT)
        self.assertEqual(cmd.action, "ask")
        self.assertEqual(cmd.instruction, "почему падает экспорт?")

    def test_command_after_mention(self):
        cmd = parse_command("@buff research откуда задержка", BOT)
        self.assertEqual(cmd.action, "research")
        self.assertEqual(cmd.instruction, "откуда задержка")

    def test_command_may_be_punctuated(self):
        self.assertEqual(parse_command("@buff task: сделай выгрузку", BOT).action, "task")

    def test_text_on_both_sides_is_kept(self):
        # People write the context first and the instruction after; dropping
        # either half loses the question.
        cmd = parse_command("Кириллица ломается, @buff разберись почему", BOT)
        self.assertEqual(cmd.instruction, "Кириллица ломается, разберись почему")

    def test_tracker_specific_mention_form(self):
        # Jira's autocomplete inserts wiki markup, not an @-handle.
        self.assertEqual(parse_command("[~buff] что тут было?", BOT).action, "ask")
        self.assertEqual(parse_command("[~USER10100] research копай", BOT).action, "research")

    def test_alias(self):
        self.assertIsNotNone(parse_command("@buff-bot привет", BOT))

    def test_longer_handle_is_not_us(self):
        self.assertIsNone(parse_command("@buffalo привет", BOT))

    def test_slash_command_is_a_direct_address(self):
        # GitHub, GitLab and Gitea speak slash. Typing /research IS addressing us.
        cmd = parse_command("/research почему тормозит отчёт", BOT)
        self.assertEqual(cmd.action, "research")
        self.assertEqual(cmd.instruction, "почему тормозит отчёт")

    def test_slash_command_on_its_own_line(self):
        cmd = parse_command("Смотри вложение.\n/ask что тут не так?", BOT)
        self.assertEqual(cmd.action, "ask")
        self.assertEqual(cmd.instruction, "Смотри вложение.\nчто тут не так?")

    def test_slash_word_boundary(self):
        self.assertIsNone(parse_command("/asking for a friend", BOT))

    def test_slash_only_at_the_start_of_a_line(self):
        # A path or a date in mid-sentence is not a command.
        self.assertIsNone(parse_command("смотри src/ask/handler.ts", BOT))

    def test_unknown_slash_command_is_not_ours(self):
        self.assertIsNone(parse_command("/assign @someone", BOT))

    def test_slash_scoped_to_the_bot(self):
        cmd = parse_command("/buff research разберись", BOT)
        self.assertEqual(cmd.action, "research")
        self.assertEqual(cmd.instruction, "разберись")

    def test_mention_with_nothing_to_do(self):
        self.assertIsNone(parse_command("@buff", BOT))
        self.assertIsNone(parse_command("/ask", BOT))

    def test_plain_text_is_not_a_request(self):
        self.assertIsNone(parse_command("надо бы позвать бота", BOT))
        self.assertIsNone(parse_command("", BOT))


class TestScope(unittest.TestCase):
    def test_empty_scope_allows_nothing(self):
        # "Nowhere", not "everywhere": an unconfigured connection must not answer
        # across a customer's whole tracker.
        self.assertFalse(Scope([]).allows("PROJ"))

    def test_listed_only(self):
        s = Scope(["PROJ", "OPS"])
        self.assertTrue(s.allows("PROJ"))
        self.assertFalse(s.allows("SECRET"))

    def test_case_folding_is_opt_in(self):
        self.assertFalse(Scope(["acme/api"]).allows("Acme/API"))
        self.assertTrue(Scope(["acme/api"], fold_case=True).allows("Acme/API"))

    def test_require_refuses_loudly(self):
        # Writes are bound by the same list as reads: the connector is the
        # customer's trust boundary in both directions.
        with self.assertRaises(RuntimeError):
            Scope(["PROJ"]).require("SECRET")
        Scope(["PROJ"]).require("PROJ")


class TestSignedIn(unittest.TestCase):
    """The check has to say WHOSE account this is: access is granted to a login,
    a mention is written at a login, and an operator comparing the two needs to
    see the one they typed."""

    def test_names_both_when_they_differ(self):
        self.assertEqual(Identity(name="buffbot", display="Buff Bot").signed_in(),
                         "Buff Bot (buffbot)")

    def test_names_it_once_when_they_are_the_same(self):
        self.assertEqual(Identity(name="buffbot", display="buffbot").signed_in(), "buffbot")

    def test_falls_back_to_what_there_is(self):
        # Confluence Cloud has no login at all; Jira's changelog has no display
        # name. Whatever the tracker gave, the check still names somebody.
        self.assertEqual(Identity(display="Buff Bot").signed_in(), "Buff Bot")
        self.assertEqual(Identity(name="buffbot").signed_in(), "buffbot")
        self.assertEqual(Identity(key="557058:abc").signed_in(), "557058:abc")


class TestCaseFolding(unittest.TestCase):
    """People capitalise names. A tracker's autocomplete does too — Jira Cloud
    inserts the display name as it is written, and a person typing by hand
    writes «@Buff». Matching the handle exactly meant those requests were not
    requests at all, silently: no answer, no error, nothing in a log."""

    bot = Identity(name="buffbot", display="Buff Bot", alias="buff")

    def test_a_capitalised_mention_still_reaches_the_bot(self):
        self.assertEqual(parse_command("@Buff что тут по срокам?", self.bot),
                         Command("ask", "что тут по срокам?"))
        self.assertEqual(parse_command("@BUFFBOT разберись", self.bot),
                         Command("ask", "разберись"))

    def test_a_longer_name_is_still_somebody_else(self):
        # Case folding must not cost the boundary: @buffalo is a different
        # person however it is capitalised.
        self.assertIsNone(parse_command("@Buffalo привет", self.bot))

    def test_a_slash_command_is_read_however_it_is_written(self):
        self.assertEqual(parse_command("/Research откуда задержка", self.bot),
                         Command("research", "откуда задержка"))
        self.assertEqual(parse_command("/Buff TASK доделать тесты", self.bot),
                         Command("task", "доделать тесты"))


class TestTime(unittest.TestCase):
    def test_offsets_and_zulu_are_the_same_instant(self):
        self.assertEqual(moment("2026-01-01T10:00:00.000+0000"),
                         moment("2026-01-01T10:00:00Z"))

    def test_after_compares_moments_not_strings(self):
        self.assertTrue(after("2026-01-01T10:00:01.000+0000", "2026-01-01T10:00:00Z"))
        self.assertFalse(after("2026-01-01T09:59:59.000+0000", "2026-01-01T10:00:00Z"))

    def test_no_watermark_means_everything_is_new(self):
        self.assertTrue(after("2026-01-01T10:00:00Z", None))

    def test_unreadable_timestamp_counts_as_new(self):
        # Acting twice on a request is recoverable; losing it is not.
        self.assertTrue(after("не дата", "2026-01-01T10:00:00Z"))
        self.assertIsNone(moment("не дата"))

    def test_a_watermark_for_a_query_is_whole_seconds_in_utc(self):
        # Redmine refuses fractions outright (422), and the others accept them by
        # luck; this is the spelling every tracker documents.
        self.assertEqual(at_second("2026-09-04T19:36:42.968468871Z"), "2026-09-04T19:36:42Z")
        self.assertEqual(at_second("2026-09-04T22:36:42.968+0300"), "2026-09-04T19:36:42Z")

    def test_a_query_window_rounds_down_never_up(self):
        # Widening the window costs a duplicate the filter throws away; narrowing
        # it loses a request outright.
        self.assertEqual(at_second("2026-09-04T19:36:42.999Z"), "2026-09-04T19:36:42Z")

    def test_no_watermark_is_no_window(self):
        self.assertEqual(at_second(None), "")
        self.assertEqual(at_second("не дата"), "")


class TestEventId(unittest.TestCase):
    def test_carries_the_moment(self):
        # A recreated tracker numbers comments from one again; without the moment
        # the platform's de-duplication swallows real events as repeats (#156).
        self.assertEqual(event_id("comment", 42, "2026-01-01T10:00:00Z"),
                         "comment:42@2026-01-01T10:00:00Z")


class TestSource(unittest.TestCase):
    def test_issue_by_default(self):
        self.assertEqual(source("PROJ-1"),
                         {"issue": "PROJ-1", "trigger": "mention", "context": "issue"})

    def test_pull_request_carries_its_branch(self):
        # The answer to a question asked in a pull request has to be computed on
        # the code of that pull request (#287).
        self.assertEqual(
            source("acme/api#7", context="pull", repo="acme/api", ref="feature/csv"),
            {"issue": "acme/api#7", "trigger": "mention", "context": "pull",
             "repo": "acme/api", "ref": "feature/csv"})

    def test_unknown_context_is_refused(self):
        with self.assertRaises(ValueError):
            source("PROJ-1", context="whatever")


if __name__ == "__main__":
    unittest.main()
