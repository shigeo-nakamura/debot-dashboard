import pathlib
import re
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "arcus-health-watch.yml"


class ArcusHealthWatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text()

    def test_live_tick_timer_is_monitored_once(self):
        loop = re.search(r"for timer in ([^;]+); do", self.workflow)
        self.assertIsNotNone(loop)
        timers = loop.group(1).split()
        self.assertEqual(timers.count("arcus-spot-live-tick"), 1)

    def test_timer_active_enabled_and_service_result_are_checked(self):
        self.assertIn('systemctl is-active "${timer}.timer"', self.workflow)
        self.assertIn('systemctl is-enabled "${timer}.timer"', self.workflow)
        self.assertIn('systemctl show "${timer}.service" -p Result', self.workflow)
        self.assertIn('timer:${name}:inactive', self.workflow)
        self.assertIn('timer:${name}:disabled', self.workflow)
        self.assertIn('timer:${name}:result_failed', self.workflow)

    def test_oneshot_service_is_not_required_to_stay_active(self):
        self.assertNotIn('systemctl is-active "${timer}.service"', self.workflow)


if __name__ == "__main__":
    unittest.main()
