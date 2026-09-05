"""Release-tool tests. Host qualification is mocked; no VM runs."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


formula = load('update-tap-formula')
promotion = load('promote-tap')
qualification = load('qualify-applevf')


def legacy(revision, version='1.0.0'):
    return f'''class Microagent < Formula
  url "https://github.com/geoffbelknap/microagent.git",
      revision: "{revision}"
  version "{version}"

  on_macos do
    resource "kernel" do
      url "https://example.com/kernel"
      sha256 "{'d' * 64}"
    end
  end

  test do
  end
end
'''


class FormulaTests(unittest.TestCase):
    def test_linux_promotion_retains_mac_and_resources(self):
        before = legacy('a' * 40)
        after = formula.update_platform(before, 'linux', 'b' * 40, '1.1.0')
        self.assertEqual(formula.source_pin(after, 'macos'), ('a' * 40, '1.0.0'))
        self.assertEqual(formula.source_pin(after, 'linux'), ('b' * 40, '1.1.0'))
        self.assertIn(before[before.index('  on_macos do'):], after)

    def test_mac_promotion_retains_linux_and_is_idempotent(self):
        split = formula.update_platform(legacy('a' * 40), 'linux', 'b' * 40, '1.1.0')
        after = formula.update_platform(split, 'macos', 'c' * 40, '1.2.0')
        self.assertEqual(formula.source_pin(after, 'linux'), ('b' * 40, '1.1.0'))
        self.assertEqual(formula.source_pin(after, 'macos'), ('c' * 40, '1.2.0'))
        self.assertEqual(after, formula.update_platform(after, 'macos', 'c' * 40, '1.2.0'))

    def test_latest_migration_uses_stable_mac_source(self):
        after = formula.update_platform(legacy('b' * 40, '1.0.0-latest.2'),
                                        'linux', 'c' * 40, '1.0.0-latest.3', legacy('a' * 40))
        self.assertEqual(formula.source_pin(after, 'macos')[0], 'a' * 40)

    def test_rejects_ambiguous_or_malformed_pins_and_injection(self):
        for text in [legacy('a' * 40) * 2, legacy('bad'),
                     legacy('a' * 40) + '# microagent source: macos',
                     formula.platform_block('linux', 'a' * 40, '1.0.0') + legacy('a' * 40)]:
            with self.assertRaises(ValueError):
                formula.update_platform(text, 'linux', 'b' * 40, '1.1.0')
        for revision, version in [('bad', '1.1.0'), ('b' * 40, '1.1.0"\nend')]:
            with self.assertRaises(ValueError):
                formula.update_platform(legacy('a' * 40), 'linux', revision, version)

    def test_platform_caveats_are_independent(self):
        text = formula.update_caveats(legacy('a' * 40), 'Mac note', 'macos', None)
        text = formula.update_caveats(text, 'Linux note', 'linux', None)
        self.assertIn('Mac note', text)
        self.assertIn('Linux note', text)
        text = formula.update_caveats(text, '', 'linux', None)
        self.assertIn('Mac note', text)
        self.assertNotIn('Linux note', text)
        self.assertEqual(text.count('def caveats'), 1)

    def test_cli_refuses_backward_promotion_without_writing(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            def git(*args):
                return subprocess.check_output(['git', '-C', tmp, *args], text=True).strip()
            git('init', '-q')
            git('-c', 'user.name=Test', '-c', 'user.email=test@example.com',
                'commit', '-qm', 'first', '--allow-empty')
            first = git('rev-parse', 'HEAD')
            git('-c', 'user.name=Test', '-c', 'user.email=test@example.com',
                'commit', '-qm', 'second', '--allow-empty')
            second = git('rev-parse', 'HEAD')
            target = repo / 'microagent.rb'
            target.write_text(legacy(second, '1.1.0'))
            caveats = repo / 'caveats'
            caveats.write_text('')
            result = subprocess.run([sys.executable, str(ROOT / 'update-tap-formula.py'),
                '--formula', str(target), '--source-repo', tmp, '--platform', 'linux',
                '--revision', first, '--version', '1.0.0', '--caveats', str(caveats)],
                capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(target.read_text(), legacy(second, '1.1.0'))


class QualificationTests(unittest.TestCase):
    def test_promotion_requires_latest_qualification_not_old_live_status(self):
        old = {'context': 'applevf-live', 'state': 'success'}
        passed = {'context': 'applevf-qualified', 'state': 'success'}
        for statuses in [[], [old], [{'context': 'applevf-qualified', 'state': 'pending'}, passed],
                         [{'context': 'applevf-qualified', 'state': 'failure'}, passed]]:
            with self.assertRaises(ValueError):
                promotion.require_qualification({'statuses': statuses})
        promotion.require_qualification({'statuses': [passed, old]})

    def test_environment_cannot_skip_or_select_external_binaries(self):
        with patch.dict(os.environ, {'MICROAGENT_APPLEVF_SUPERVISOR': '/old/binary',
                                    'MICROAGENT_E2E_REQUIRE_VM': '0',
                                    'MICROAGENT_E2E_SKIP_BUILD': '1'}):
            env = qualification.clean_environment()
        self.assertNotIn('MICROAGENT_APPLEVF_SUPERVISOR', env)
        self.assertNotIn('MICROAGENT_E2E_SKIP_BUILD', env)
        self.assertEqual(env['MICROAGENT_E2E_REQUIRE_VM'], '1')

    def test_explicit_model_fixture_survives_without_runner_overrides(self):
        with patch.dict(os.environ, {'MICROAGENT_MODEL_RUNNER_ARGS': '--unexpected',
                                    'MICROAGENT_LLAMA_SERVER': '/unselected'}):
            env = qualification.clean_environment('/selected/llama-server')
        self.assertEqual(env['MICROAGENT_LLAMA_SERVER'], '/selected/llama-server')
        self.assertNotIn('MICROAGENT_MODEL_RUNNER_ARGS', env)

    def test_candidate_environment_drops_credentials_and_startup_hooks(self):
        private = {key: 'private-fixture-value' for key in (
            'GH_TOKEN', 'GITHUB_TOKEN', 'AWS_SECRET_ACCESS_KEY', 'OPENAI_API_KEY',
            'HF_TOKEN', 'SSH_AUTH_SOCK', 'REGISTRY_AUTH_FILE', 'HTTP_PROXY',
            'HTTPS_PROXY', 'BASH_ENV', 'ENV', 'DYLD_INSERT_LIBRARIES',
            'PYTHONPATH', 'GOFLAGS', 'GOPROXY', 'GIT_CONFIG_COUNT',
            'SOME_FUTURE_CREDENTIAL',
        )}
        with patch.dict(os.environ, {**private, 'PATH': '/usr/bin:/bin', 'HOME': '/fixture/home'}, clear=True):
            env = qualification.clean_environment('/fixture/llama-server')
        self.assertTrue(set(private).isdisjoint(env))
        self.assertEqual(env['HOME'], '/fixture/home')
        self.assertEqual(env['GOENV'], 'off')
        self.assertEqual(env['GIT_CONFIG_GLOBAL'], os.devnull)
        # Prove the environment passed to a real child contains no sentinel.
        child = subprocess.check_output([sys.executable, '-c',
                                        'import json, os; print(json.dumps(dict(os.environ)))'],
                                       env=env, text=True)
        self.assertNotIn('private-fixture-value', child)
        self.assertEqual(json.loads(child)['MICROAGENT_LLAMA_SERVER'], '/fixture/llama-server')

    def test_public_status_payload_contains_no_diagnostics(self):
        for state in ('pending', 'success', 'failure'):
            with patch.object(qualification.subprocess, 'run') as run:
                qualification.status('a' * 40, state)
            self.assertEqual(run.call_args.args[0], [
                'gh', 'api', 'repos/geoffbelknap/microagent/statuses/' + 'a' * 40,
                '-f', f'state={state}', '-f', 'context=applevf-qualified',
                '-f', f'description=Apple VF qualification {state}',
            ])
        with patch.object(qualification.subprocess, 'run') as run:
            for sha, state in [('a' * 40, 'failure: /private/log'), ('/private/log', 'failure')]:
                with self.assertRaises(ValueError):
                    qualification.status(sha, state)
            run.assert_not_called()

    def test_mainline_gate_rejects_unmerged_commits_and_tags(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            def git(*args):
                return subprocess.check_output(['git', '-c', 'commit.gpgsign=false',
                                                '-c', 'user.name=Fixture',
                                                '-c', 'user.email=fixture@example.invalid',
                                                *args], cwd=repo, text=True).strip()
            git('init', '-q', '-b', 'main')
            git('commit', '-q', '--allow-empty', '-m', 'main')
            main = git('rev-parse', 'HEAD')
            git('update-ref', 'refs/remotes/origin/main', main)
            qualification.require_mainline(repo, main)
            git('checkout', '-q', '-b', 'candidate')
            git('commit', '-q', '--allow-empty', '-m', 'unmerged')
            git('tag', 'v1.0.0')
            for ref in ('HEAD', 'v1.0.0'):
                with self.assertRaisesRegex(ValueError, 'main history'):
                    qualification.require_mainline(repo, git('rev-parse', ref))

    def test_model_fixture_requires_executable(self):
        with tempfile.TemporaryDirectory() as tmp:
            runner = Path(tmp) / 'llama-server'
            runner.write_text('fixture')
            with self.assertRaisesRegex(ValueError, 'not executable'):
                qualification.resolve_llama_server(str(runner))
            runner.chmod(0o700)
            self.assertEqual(qualification.resolve_llama_server(str(runner)), str(runner))

    def test_step_streams_and_retains_output_and_propagates_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            capture = io.StringIO()
            with (Path(tmp) / 'run.log').open('w') as log, contextlib.redirect_stdout(capture):
                qualification.run_step([sys.executable, '-c', 'print("first")'], Path(tmp), dict(os.environ), log)
                with self.assertRaises(subprocess.CalledProcessError) as failure:
                    qualification.run_step([sys.executable, '-c', 'print("failed"); raise SystemExit(7)'], Path(tmp), dict(os.environ), log)
            self.assertEqual(failure.exception.returncode, 7)
            for text in ('first\n', 'failed\n'):
                self.assertIn(text, capture.getvalue())
                self.assertIn(text, (Path(tmp) / 'run.log').read_text())

    def exercise_run(self, failure=False, record=True, unmerged=False):
        # Mock host detection and every process call. This verifies control flow
        # and durable reporting; it is not physical-host qualification evidence.
        with tempfile.TemporaryDirectory() as tmp:
            destination = Path(tmp) / 'report'
            argv = ['qualify', '--ref', 'b' * 40, '--output-dir', str(destination)]
            if record:
                argv.append('--record')
            def output(*args, **kwargs):
                if args[1:3] == ('remote', 'get-url'):
                    return 'https://github.com/geoffbelknap/microagent.git'
                if args[1] == 'rev-parse':
                    return 'b' * 40
                return ''
            def effect(command, checkout, env, log):
                if failure:
                    raise subprocess.CalledProcessError(1, ['test'])
                if command[0] == 'scripts/dev/build-local.sh':
                    binaries = checkout / '.build' / 'dev'
                    binaries.mkdir(parents=True)
                    for name in ['microagent', 'microagent-guestinit-arm64', 'microagent-applevf-supervisor']:
                        (binaries / name).write_bytes(b'mocked build output')
            with patch.object(sys, 'argv', argv), \
                 patch.object(qualification.platform, 'system', return_value='Darwin'), \
                 patch.object(qualification.platform, 'machine', return_value='arm64'), \
                 patch.object(qualification.shutil, 'which', return_value='/tool'), \
                 patch.object(qualification, 'resolve_llama_server', return_value='/fixture/llama-server'), \
                 patch.object(qualification, 'require_mainline', side_effect=ValueError('not mainline') if unmerged else None), \
                 patch.object(qualification, 'output', side_effect=output), \
                 patch.object(qualification.subprocess, 'run'), \
                 patch.object(qualification, 'run_step', side_effect=effect) as steps, \
                 patch.object(qualification, 'status') as statuses, \
                 contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()):
                if unmerged:
                    with self.assertRaises(SystemExit) as error:
                        qualification.main()
                    self.assertEqual(error.exception.code, 2)
                    self.assertFalse(destination.exists())
                    steps.assert_not_called()
                    statuses.assert_not_called()
                    return
                code = qualification.main()
            result = json.loads((destination / 'result.json').read_text())
            self.assertEqual(code, 1 if failure else 0)
            self.assertEqual(result['commit'], 'b' * 40)
            self.assertEqual(result['status'], 'failure' if failure else 'success')
            if record:
                self.assertEqual([call.args[1] for call in statuses.call_args_list],
                                 ['pending', result['status']])
            else:
                statuses.assert_not_called()
            if not failure:
                commands = [call.args[0] for call in steps.call_args_list]
                self.assertIn(['scripts/dev/cleanup-temp.sh'], commands)
                self.assertTrue(any('--require-vm' in command for command in commands))

    def test_success_records_after_suite(self):
        self.exercise_run()

    def test_failure_replaces_prior_success_and_retains_report(self):
        self.exercise_run(failure=True)

    def test_local_run_never_posts_status(self):
        self.exercise_run(record=False)

    def test_unmerged_candidate_never_runs_or_posts_status(self):
        self.exercise_run(unmerged=True)


if __name__ == '__main__':
    unittest.main()
