from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "arcus-gas-watch.yml"
RUNBOOK = ROOT / "deploy" / "arcus-gas-watch.md"


class ArcusGasWatchWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.runbook = RUNBOOK.read_text(encoding="utf-8")

    def test_configuration_uses_repository_vars_without_api_lookup(self):
        self.assertIn(
            "ARCUS_WALLET_ADDRESS: ${{ vars.ARCUS_WALLET_ADDRESS }}",
            self.workflow,
        )
        self.assertIn(
            "ARCUS_WALLET_EVER_FUNDED: ${{ vars.ARCUS_WALLET_EVER_FUNDED }}",
            self.workflow,
        )
        self.assertIn("permissions: {}", self.workflow)
        self.assertNotIn("gh variable", self.workflow)
        self.assertNotIn("github.token", self.workflow)

    def test_configuration_fails_closed_before_rpc_read(self):
        validation = self.workflow.index(
            "- name: Validate Arcus gas-watch configuration"
        )
        rpc_read = self.workflow.index("- name: Read Arcus wallet gas balance")
        self.assertLess(validation, rpc_read)
        self.assertIn(
            '[[ ! "$ARCUS_WALLET_ADDRESS" =~ ^0x[0-9a-fA-F]{40}$ ]]',
            self.workflow,
        )
        self.assertIn('case "$ARCUS_WALLET_EVER_FUNDED" in', self.workflow)
        self.assertIn("true|false) ;;", self.workflow)
        self.assertNotIn("steps.funded.outputs", self.workflow)

    def test_retired_wallet_is_not_embedded_in_workflow(self):
        self.assertNotIn(
            "0x53cb889b5f3928921abb5ec413a2501cbf6170c6",
            self.workflow.lower(),
        )
        self.assertIn(
            "0x812B6A6da8E0dF1fBCA7939ae32089Cf85c5DF05",
            self.runbook,
        )


if __name__ == "__main__":
    unittest.main()
