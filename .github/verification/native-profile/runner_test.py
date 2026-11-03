from collections import Counter
import unittest

from runner import parse_profile_symbols


class ParseProfileSymbolsTest(unittest.TestCase):
    def test_counts_symbols_without_returning_process_addresses(self) -> None:
        script = "\n".join(
            (
                "# perf script sample output",
                "comm pid tid cpu time: event:",
                "not-an-ip sqlite3_false_positive",
                "0x00001000 sqlite3_exec",
                "0x00002000 runtime.cgocall",
                "0x00003000 github.com/mattn/go-sqlite3.(*SQLiteConn).Step",
                "0x00004000 [unknown]",
                "0x00005000",
                "00006000 sqlite3_prepare_v2",
            )
        )

        perf_samples, resolved, unresolved, native, observed, go_samples = (
            parse_profile_symbols(script)
        )

        self.assertEqual(perf_samples, 6)
        self.assertEqual(resolved, 4)
        self.assertEqual(unresolved, 2)
        self.assertEqual(
            native, Counter({"sqlite3_exec": 1, "sqlite3_prepare_v2": 1})
        )
        self.assertEqual(
            observed,
            Counter(
                {
                    "sqlite3_exec": 1,
                    "runtime.cgocall": 1,
                    "github.com/mattn/go-sqlite3.(*SQLiteConn).Step": 1,
                    "sqlite3_prepare_v2": 1,
                }
            ),
        )
        self.assertEqual(go_samples, 2)
        self.assertTrue(all("0x" not in symbol for symbol in observed))

    def test_dso_names_do_not_count_as_sqlite_functions(self) -> None:
        _, _, _, native, _, _ = parse_profile_symbols("0x00006000 libsqlite3.so")

        self.assertEqual(native, Counter())


if __name__ == "__main__":
    unittest.main()
