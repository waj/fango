import unittest

from compare import interval, selection, summarize


class StatisticsTests(unittest.TestCase):
    def test_selects_one_representation_for_all_cases(self):
        for checked, unsafe, selected, verdict in (
            (["pass"], ["pass"], "checked", "pass"),
            (["fail"], ["pass"], "unsafe", "pass"),
            (["pass", "fail"], ["fail", "pass"], None, "fail"),
            (["inconclusive"], ["fail"], None, "inconclusive"),
        ):
            rows = [dict(variant=v, verdict=r) for v, results in (("checked", checked), ("unsafe", unsafe)) for r in results]
            result = selection(rows)
            self.assertEqual(result["selected"], selected)
            self.assertEqual(result["verdict"], verdict)

    def test_interval_is_reproducible_and_uses_paired_ratios(self):
        ratios = [0.8, 0.9, 1.0, 1.1, 1.2] * 4
        self.assertEqual(interval(ratios), interval(ratios))
        median, lower, upper = interval(ratios)
        self.assertEqual(median, 1.0)
        self.assertLessEqual(lower, median)
        self.assertGreaterEqual(upper, median)

    def test_selected_variants_keep_inconclusive_cases_visible(self):
        for verdict in ("pass", "fail", "inconclusive"):
            result = selection([
                dict(variant="opened", verdict="pass"),
                dict(variant="opened", verdict=verdict),
            ], ("explicit", "opened"))
            self.assertEqual(result["verdict"], verdict)
            self.assertEqual(result["selected"], "opened" if verdict == "pass" else None)
            self.assertEqual(result["candidates"], {"opened": verdict})

    def test_gate_accounts_for_repetitions_and_has_explicit_failure(self):
        for ratio, verdict in ((1.0, "pass"), (1.2, "fail")):
            rows = []
            for sample in range(20):
                rows.extend([
                    dict(sample=sample, variant="explicit", NS=100, Reps=1, Bytes=0, Allocs=0),
                    dict(sample=sample, variant="checked", NS=200 * ratio, Reps=2, Bytes=32, Allocs=2),
                ])
            result = summarize(rows, "checked")
            self.assertEqual(result["ratio"], ratio)
            self.assertEqual(result["verdict"], verdict)
            self.assertEqual(result["checked"]["Allocs"], 1)


if __name__ == "__main__":
    unittest.main()
