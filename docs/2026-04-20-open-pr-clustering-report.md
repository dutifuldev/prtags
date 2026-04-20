---
title: Open PR Clustering Report
date: 2026-04-20
repository: openclaw/openclaw
sample_size: 100
---

# Open PR Clustering Report

## Summary

- Processed all 100 PRs returned by the current `ghr` open-PR list for `openclaw/openclaw`.
- Created 12 `prtags` groups covering 25 PRs.
- Left 75 PRs ungrouped because this pass did not find a strong enough duplicate or same-issue-family signal.

## Method

- Used `ghr` data only: open PR titles and bodies from the mirrored GitHub read surface, plus body-level issue cross-references and explicit `replaces` / `companion` language.
- Used `ghr` similarity/search opportunistically where index coverage existed, but the primary signal for this pass was title/body/issue-reference evidence because the newest PRs were not consistently searchable yet.
- Did not use `pr-search-cli`.

## Created Groups

### `eager-piglet-i6pr` duplicate

Open PR duplicate: Mission Control v3.0 UI updates

- [#68861](https://github.com/openclaw/openclaw/pull/68861)
- [#68756](https://github.com/openclaw/openclaw/pull/68756)

Exact title match; `#68861` explicitly says it replaces `#68756` after a clean rebuild.

### `legible-ferret-wc2p` duplicate

Open PR duplicate: auto-reply queue orphaning

- [#68914](https://github.com/openclaw/openclaw/pull/68914)
- [#68908](https://github.com/openclaw/openclaw/pull/68908)
- [#68839](https://github.com/openclaw/openclaw/pull/68839)

`#68914` and `#68908` have the same title and fix the same `FOLLOWUP_QUEUES` identity-guard bug; `#68839` addresses the same `#68838` late-drain queue deletion failure mode.

### `living-burro-rkj0` duplicate

Open PR duplicate: browser tool via `/tools/invoke`

- [#68911](https://github.com/openclaw/openclaw/pull/68911)
- [#68879](https://github.com/openclaw/openclaw/pull/68879)

Same browser-tool `/tools/invoke` bug and near-identical root-cause description.

### `evolving-spaniel-9ojm` duplicate

Open PR duplicate: gateway `nodeWakeById` no-registration cleanup

- [#68912](https://github.com/openclaw/openclaw/pull/68912)
- [#68848](https://github.com/openclaw/openclaw/pull/68848)

Same stale `nodeWakeById` leak on the no-registration early return; both reference `#68847`.

### `actual-grouse-o3j8` duplicate

Open PR duplicate: onboard Homebrew prompt on unsupported platforms

- [#68910](https://github.com/openclaw/openclaw/pull/68910)
- [#68894](https://github.com/openclaw/openclaw/pull/68894)

Same `#68893` bug; one patch is FreeBSD-specific and the other broadens it to all unsupported platforms.

### `modern-rhino-knzo` duplicate

Open PR duplicate: gateway `costUsageCache` growth

- [#68913](https://github.com/openclaw/openclaw/pull/68913)
- [#68842](https://github.com/openclaw/openclaw/pull/68842)

Same `#68841` cache-growth problem with alternative eviction strategies.

### `crisp-shark-7utq` duplicate

Open PR duplicate: allowlist heredoc approval gate

- [#68854](https://github.com/openclaw/openclaw/pull/68854)
- [#68824](https://github.com/openclaw/openclaw/pull/68824)

Same `#68661` heredoc-approval regression; one is narrower, one removes the blanket gate.

### `able-shepherd-ttf8` duplicate

Open PR duplicate: streaming usage for non-default openai-completions

- [#68749](https://github.com/openclaw/openclaw/pull/68749)
- [#68742](https://github.com/openclaw/openclaw/pull/68742)

Same `#68707` usage-tracking regression for local/custom OpenAI-compatible providers.

### `patient-piglet-45hj` duplicate

Open PR duplicate: Windows `claude` cmd shim spawning

- [#68849](https://github.com/openclaw/openclaw/pull/68849)
- [#68819](https://github.com/openclaw/openclaw/pull/68819)

Same `#68788` Windows `claude-cli` spawn failure; `#68849` is the narrower allowlist subset of `#68819`.

### `legal-wolf-dmw6` duplicate

Open PR duplicate: Gemma 4 reasoning detection

- [#68762](https://github.com/openclaw/openclaw/pull/68762)
- [#68740](https://github.com/openclaw/openclaw/pull/68740)

Same `#68728` Gemma 4 reasoning-model detection gap.

### `exciting-crappie-yw0w` issue-family

Open PR cluster: memory dreaming noise from `#68449`

- [#68876](https://github.com/openclaw/openclaw/pull/68876)
- [#68870](https://github.com/openclaw/openclaw/pull/68870)

`#68876` explicitly says it is a companion to `#68870`; both address different halves of `#68449`.

### `discrete-airedale-t4tc` issue-family

Open PR cluster: preserve TTS transcript text

- [#68877](https://github.com/openclaw/openclaw/pull/68877)
- [#68869](https://github.com/openclaw/openclaw/pull/68869)

`#68877` explicitly says it is independent of and complementary to `#68869`; both preserve spoken text at different layers.

## Comparison Against `pr-search-cli`

In the table below, `Slopfarmer clustering` means the current `pr-search-cli` issue-cluster output.

| Group | Agent clustering (Codex + `ghr`) | Slopfarmer clustering | Winner | Notes |
| --- | --- | --- | --- | --- |
| `eager-piglet-i6pr` Mission Control v3.0 UI updates | [#68861](https://github.com/openclaw/openclaw/pull/68861), [#68756](https://github.com/openclaw/openclaw/pull/68756) | none | Agent | Exact title match and explicit `replaces` language in `#68861`. `pr-search-cli` misses the pair entirely. |
| `legible-ferret-wc2p` auto-reply queue orphaning | [#68914](https://github.com/openclaw/openclaw/pull/68914), [#68908](https://github.com/openclaw/openclaw/pull/68908), [#68839](https://github.com/openclaw/openclaw/pull/68839) | none | Agent | Strong current-open-PR duplicate set around the same `FOLLOWUP_QUEUES` late-drain bug. `pr-search-cli` has no matching issue cluster yet. |
| `living-burro-rkj0` browser tool via `/tools/invoke` | [#68911](https://github.com/openclaw/openclaw/pull/68911), [#68879](https://github.com/openclaw/openclaw/pull/68879) | none | Agent | Near-identical root-cause descriptions and fix target. `pr-search-cli` currently misses the pair. |
| `evolving-spaniel-9ojm` gateway `nodeWakeById` no-registration cleanup | [#68912](https://github.com/openclaw/openclaw/pull/68912), [#68848](https://github.com/openclaw/openclaw/pull/68848) | none | Agent | Both PRs fix the same stale `nodeWakeById` leak and reference `#68847`. `pr-search-cli` does not cluster them. |
| `actual-grouse-o3j8` Homebrew prompt on unsupported platforms | [#68910](https://github.com/openclaw/openclaw/pull/68910), [#68894](https://github.com/openclaw/openclaw/pull/68894) | `cluster-68893-5`: [#68894](https://github.com/openclaw/openclaw/pull/68894), [#68941](https://github.com/openclaw/openclaw/pull/68941), [#68943](https://github.com/openclaw/openclaw/pull/68943), [#69002](https://github.com/openclaw/openclaw/pull/69002) | Agent | `pr-search-cli` is broader on issue-family coverage, but it misses our direct duplicate pair because it does not include `#68910`. |
| `modern-rhino-knzo` gateway `costUsageCache` growth | [#68913](https://github.com/openclaw/openclaw/pull/68913), [#68842](https://github.com/openclaw/openclaw/pull/68842) | none | Agent | Same `#68841` bug with alternative eviction strategies. `pr-search-cli` misses the pair. |
| `crisp-shark-7utq` allowlist heredoc approval gate | [#68854](https://github.com/openclaw/openclaw/pull/68854), [#68824](https://github.com/openclaw/openclaw/pull/68824) | `cluster-68661-4`: [#68754](https://github.com/openclaw/openclaw/pull/68754), [#68824](https://github.com/openclaw/openclaw/pull/68824), [#68854](https://github.com/openclaw/openclaw/pull/68854) | Slopfarmer | It contains our pair and also picks up older related PR `#68754`, so it is more complete on the issue family. |
| `able-shepherd-ttf8` streaming usage for non-default openai-completions | [#68749](https://github.com/openclaw/openclaw/pull/68749), [#68742](https://github.com/openclaw/openclaw/pull/68742) | `cluster-68707-3`: [#68742](https://github.com/openclaw/openclaw/pull/68742), [#68749](https://github.com/openclaw/openclaw/pull/68749) | Both | Exact agreement: same two PRs, same underlying `#68707` issue. |
| `patient-piglet-45hj` Windows `claude` cmd shim spawning | [#68849](https://github.com/openclaw/openclaw/pull/68849), [#68819](https://github.com/openclaw/openclaw/pull/68819) | `cluster-68788-4`: [#68792](https://github.com/openclaw/openclaw/pull/68792), [#68819](https://github.com/openclaw/openclaw/pull/68819), [#68849](https://github.com/openclaw/openclaw/pull/68849) | Slopfarmer | It contains our pair and also captures `#68792`, which belongs to the same Windows `claude` spawn issue family. |
| `legal-wolf-dmw6` Gemma 4 reasoning detection | [#68762](https://github.com/openclaw/openclaw/pull/68762), [#68740](https://github.com/openclaw/openclaw/pull/68740) | `cluster-68193-4`: [#68193](https://github.com/openclaw/openclaw/pull/68193), [#68740](https://github.com/openclaw/openclaw/pull/68740), [#68762](https://github.com/openclaw/openclaw/pull/68762) | Slopfarmer | It contains our pair and also includes older PR `#68193`, so it is broader on the same issue family. |
| `exciting-crappie-yw0w` memory dreaming noise from `#68449` | [#68876](https://github.com/openclaw/openclaw/pull/68876), [#68870](https://github.com/openclaw/openclaw/pull/68870) | `cluster-68449-4`: [#68473](https://github.com/openclaw/openclaw/pull/68473), [#68870](https://github.com/openclaw/openclaw/pull/68870), [#68876](https://github.com/openclaw/openclaw/pull/68876) | Slopfarmer | It contains our companion pair and also picks up `#68473`, making the issue-family cluster more complete. |
| `discrete-airedale-t4tc` preserve TTS transcript text | [#68877](https://github.com/openclaw/openclaw/pull/68877), [#68869](https://github.com/openclaw/openclaw/pull/68869) | none | Agent | Explicit complementary relationship in the PR body, but no current `pr-search-cli` issue cluster support. |

## Full 100-PR Disposition

| PR | Result | Notes |
| --- | --- | --- |
| `#68926` ui(chat): add agent switcher to chat controls | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68925` ui(approval): refresh overview and tighten approval actions | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68924` ui(overview): clarify usage cost wording and summary cards | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68922` fix(discord): honor native command allowlists | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68919` fix(tools): remove provider prefix from vision model config lookup | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68918` fix(telegram): require explicit exec approvers | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68915` tasks: add detached runtime plugin registration contract | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68914` fix(auto-reply): add identity guard in drain finally to prevent queue orphaning | `duplicate` `legible-ferret-wc2p` | Open PR duplicate: auto-reply queue orphaning |
| `#68913` fix(gateway): add lazy eviction to costUsageCache | `duplicate` `modern-rhino-knzo` | Open PR duplicate: gateway costUsageCache growth |
| `#68912` fix(gateway): clean up nodeWakeById entry on no-registration early return | `duplicate` `evolving-spaniel-9ojm` | Open PR duplicate: gateway nodeWakeById no-registration cleanup |
| `#68911` fix: allow browser tool via HTTP API /tools/invoke | `duplicate` `living-burro-rkj0` | Open PR duplicate: browser tool via `/tools/invoke` |
| `#68910` fix(onboard): skip Homebrew prompt on FreeBSD | `duplicate` `actual-grouse-o3j8` | Open PR duplicate: onboard Homebrew prompt on unsupported platforms |
| `#68909` fix(daemon): probe system bus and cgroup-aware dedup for gateway status | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68908` fix(auto-reply): add identity guard in drain finally to prevent queue orphaning | `duplicate` `legible-ferret-wc2p` | Open PR duplicate: auto-reply queue orphaning |
| `#68906` fix(media): avoid double-prefixing qualified image models | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68903` fix(amazon-bedrock-mantle): refresh IAM bearer token via resolveConfigApiKey cache lookup | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68894` fix(onboard): don't suggest Homebrew on non-macOS/Linux platforms (closes `#68893`) | `duplicate` `actual-grouse-o3j8` | Open PR duplicate: onboard Homebrew prompt on unsupported platforms |
| `#68889` feat(discord-surface): multi-phase thread-bound ACP delivery overhaul | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68887` fix: remove unnecessary explicit undefined on optional parameter | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68885` feat: implement video frame extraction skill using ffmpeg-static and ffprobe-installer | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68879` fix: allow browser tool to work via HTTP API /tools/invoke | `duplicate` `living-burro-rkj0` | Open PR duplicate: browser tool via `/tools/invoke` |
| `#68878` [codex] Optimize gateway session listing before full row construction | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68877` feat(auto-reply): preserve TTS transcript on audio-as-voice payloads | `issue-family` `discrete-airedale-t4tc` | Open PR cluster: preserve TTS transcript text |
| `#68876` fix(memory): filter cron-triggered sessions and `NO_REPLY` sentinels from dreaming corpus (addresses `#68449`) | `issue-family` `exciting-crappie-yw0w` | Open PR cluster: memory dreaming noise from `#68449` |
| `#68872` docs(acp): clarify Claude/Codex settings inheritance | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68870` fix(memory): expand English stopword list for concept vocabulary | `issue-family` `exciting-crappie-yw0w` | Open PR cluster: memory dreaming noise from `#68449` |
| `#68869` fix(agents): preserve spoken text in tts tool result | `issue-family` `discrete-airedale-t4tc` | Open PR cluster: preserve TTS transcript text |
| `#68866` fix(auth): invalidate stale runtime auth snapshots when auth files change | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68865` fix(feishu): add application-level WebSocket reconnection with backoff | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68861` feat: integrate Mission Control v3.0 UI updates | `duplicate` `eager-piglet-i6pr` | Open PR duplicate: Mission Control v3.0 UI updates |
| `#68860` Agents: emit turn events from embedded sessions | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68859` feat(exec): inject `OPENCLAW_SESSION_KEY` env var for child processes | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68857` feat: add `gateway.nodes.invokeTimeoutMs` config option | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68855` fix: guard against non-string content delta and thinking blocks | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68854` fix(security): allow quoted heredocs in allowlist mode without extra approval | `duplicate` `crisp-shark-7utq` | Open PR duplicate: allowlist heredoc approval gate |
| `#68853` fix(gateway): `SIGUSR1` restart fast path that doesn't break Windows `schtasks` | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68852` fix(memory-wiki): skip fenced code blocks when extracting wiki links | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68850` fix(ui): clean synthetic session selector labels | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68849` fix: add `claude` to Windows `cmdCommands` shim allowlist | `duplicate` `patient-piglet-45hj` | Open PR duplicate: Windows `claude` cmd shim spawning |
| `#68848` fix(gateway): clear `nodeWakeById` on no-registration early-return | `duplicate` `evolving-spaniel-9ojm` | Open PR duplicate: gateway `nodeWakeById` no-registration cleanup |
| `#68845` Telegram: unskip sticker e2e tests | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68843` fix(acp): treat missing `cwd` as stale bound session | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68842` fix(gateway): bound `costUsageCache` with `MAX` + `FIFO` eviction | `duplicate` `modern-rhino-knzo` | Open PR duplicate: gateway `costUsageCache` growth |
| `#68839` fix(auto-reply): guard `FOLLOWUP_QUEUES` delete against late drain finally | `duplicate` `legible-ferret-wc2p` | Open PR duplicate: auto-reply queue orphaning |
| `#68837` fix(active-memory): skip non-string entries in `pluginConfig.agents` du… | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68836` fix(telegram): add success log lines for media send operations [AI-assisted] | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68835` fix(macos): stop evaluating model catalogs as JS | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68834` fix: prevent Discord ACP binding silent hang on fresh gateway boot | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68833` fix(telegram): preserve `customCommands` priority in menu budget trimming | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68831` perf: share `JITI` instances across plugins with identical alias configs | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68830` fix(memory): expose `vectorScore` and `textScore` in hybrid search results | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68824` fix: remove blanket heredoc approval gate in allowlist mode | `duplicate` `crisp-shark-7utq` | Open PR duplicate: allowlist heredoc approval gate |
| `#68822` feat(memory): make embedding retry/concurrency parameters configurable | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68819` fix: resolve Windows `.cmd` shims to underlying `.exe` before spawn | `duplicate` `patient-piglet-45hj` | Open PR duplicate: Windows `claude` cmd shim spawning |
| `#68816` feat(moonshot): default to Kimi K2.6 with K2.6-only `thinking.keep` support | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68813` fix(config): validate plugin-owned model providers | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68808` Jdc4429 custom build | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68807` fix(codex): bridge MCP tool approval elicitations | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68803` Repo meta: add `.planning/` GSD workflow scaffold | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68801` Gateway: prune orphaned `agentRunStarts` entries | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68800` fix: route logs to stderr in `openclaw mcp serve` (fixes `#68587`) | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68798` fix: prevent auto-fallback model from persisting into session state | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68792` fix(process): shim `claude` on Windows child spawns | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68785` feat: add circuit breaker for command lane saturation | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68774` fix(memory-core): prevent staged dream candidates from leaking into `MEMORY.md` | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68773` fix(active-memory): skip payload-less `memory_search` toolResults in tr… | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68765` fix(gateway): preserve chat history across compaction checkpoint chains | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68763` fix: strip `reasoning_content` from conversation history for Gemma 4 models | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68762` fix: detect Gemma 4 models as reasoning models | `duplicate` `legal-wolf-dmw6` | Open PR duplicate: Gemma 4 reasoning detection |
| `#68756` feat: integrate Mission Control v3.0 UI updates | `duplicate` `eager-piglet-i6pr` | Open PR duplicate: Mission Control v3.0 UI updates |
| `#68755` security(healthcheck): probe `ufw`/`firewall-cmd` via safe `PATH` and config fallback | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68753` fix: allow `systemEvent` cron jobs to specify custom `timeoutSeconds` | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68750` fix: handle `MiniMax` `prompt_cache_hit_tokens` to prevent token double-counting | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68749` fix: enable `stream_options.include_usage` for local openai-completions endpoints | `duplicate` `able-shepherd-ttf8` | Open PR duplicate: streaming usage for non-default openai-completions |
| `#68748` fix: enforce `chmod 0o600` after atomic rename of config file | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68745` fix(slack): share HTTP route registry via `globalThis` across module instances | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68744` fix(whatsapp): respect `audioAsVoice` flag in outbound delivery | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68742` fix(agents): restore streaming usage tracking for non-native openai-completions providers | `duplicate` `able-shepherd-ttf8` | Open PR duplicate: streaming usage for non-default openai-completions |
| `#68741` fix(acpx): avoid per-session MCP on openclaw bridge | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68740` Ollama/models: detect Gemma 4 and `thinking` capability as reasoning | `duplicate` `legal-wolf-dmw6` | Open PR duplicate: Gemma 4 reasoning detection |
| `#68737` fix(mattermost): auto-reconnect WebSocket after consecutive health check failures | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68734` feat(hooks): allow prompt hooks to dynamically narrow tool surface | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68730` feat(amazon-bedrock-mantle): add Claude Opus 4.7 via per-model Anthropic Messages API override | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68726` fix(subagent): include role, session key, and timing in error payloads | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68725` feat(amazon-bedrock-mantle): add known context windows for open-weight Mantle models | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68724` fix(bluebubbles): preserve pinned dispatcher for media fetches | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68721` fix(codex): default app-server approvals to on-request | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68718` minions: durable SQLite-backed job queue for subagents, ACP, CLI, and cron | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68717` fix(cron): enable failure alerts by default for recurring jobs | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68716` fix: Discord guild-admin actions execute without requester... | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68714` fix(config): `openclaw.json` written with `0664` mode instead of `0600` after hot-save | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68711` fix(openai-completions): enable streaming usage for `vLLM` + local OpenAI-compat servers | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68710` fix(discord): enforce guild guards for `/new` and `/reset` | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68708` [codex] Guard browser route continue race | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68702` fix(docker): enable `host.docker.internal` for local providers | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68701` fix: strip invalid webchat delivery recipients | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68700` fix(agents): stop treating session lock waits as timeout | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68699` fix(lmstudio): bound preload cooldown state | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68698` fix(auto-reply): commit inbound dedupe on handled exits | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
| `#68694` FEAT: Add tts and image generation support to xai extension | `singleton` | No strong duplicate or same-issue-family match found in this pass. |
