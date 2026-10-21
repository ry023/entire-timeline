# MVP実装・検証記録

## 調査した公式ソース

確認日：2026-10-21。Entire公式リポジトリの`main`を取得し、コミット`99d28955d5d24f589f5cd09d921a3356cae60e33`で以下を確認した。実装時にローカルのEntire CLI 0.9.0の`checkpoint explain --help`も照合した。

| 確認対象 | 確認結果と実装への反映 |
| --- | --- |
| [sessions-and-checkpoints.md](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/docs/architecture/sessions-and-checkpoints.md) | CheckpointのSession配列と累積生履歴を確認。Session IDで統合し、配列のindexを同一性に使わない |
| [api/checkpoint/metadata.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/api/checkpoint/metadata.go) | `checkpoint_transcript_start`は`full.jsonl`の行位置。`GetTranscriptStart`は正の新フィールドを優先し、それ以外では`transcript_lines_at_start`を使う。0は`omitempty`で省略され得る |
| 同上の`CompactTranscriptStart`と`SessionFilePaths` | `transcript.jsonl`は別座標。新しい圧縮履歴は累積、マーカー不在の旧形式は差分。圧縮時に境界をまたぐ1行の重複も起こり得るため、生履歴の行位置と混用しない |
| 同上の`CommitSHA`と`LinkedCommits` | トレーラーのない関連付けを収集。`commit_sha`はインポート時点のアンカー、`linked_commits`はトレーラーと同等の関連付けとして扱う |
| [Copilot transcript.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/cmd/entire/cli/agent/copilotcli/transcript.go) | `GetTranscriptPosition`は空行を含む物理行数。末尾改行のない最後の行も数える。イベントは`type`、`timestamp`、`id`、`data`を持つ |
| [transcript/copilot.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/transcript/copilot.go) | ネイティブのUser本文は`data.content`。Assistantは`content`と`toolRequests`、結果は別イベント。圧縮版では結果をブロックに埋め込む。復元側ではこれらを破棄しない |
| [Claude Code claude.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/cmd/entire/cli/agent/claudecode/claude.go) | 行位置の単位がCopilotと同じJSONL物理行数であるため、共通区間抽出を利用。`message.content`の解析は形式ごとに分離 |
| [checkpoint/persistent.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/cmd/entire/cli/checkpoint/persistent.go)・[agent/chunking.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/cmd/entire/cli/agent/chunking.go) | 生履歴はチャンク0が`full.jsonl`、以後`.001`等。JSONLチャンクは改行1文字で連結する。読み取り側で連番欠落を検出する |
| [checkpoint/refs_naming.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/cmd/entire/cli/checkpoint/refs_naming.go)・[id/id.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/cmd/entire/cli/checkpoint/id/id.go) | git-refsはIDの末尾2文字でshardし、tree rootにCheckpoint内容を保存。v1ブランチの先頭2文字による配置と区別。12桁小文字hexと26桁の正規ULIDを扱う |
| [explain_export.go](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/cmd/entire/cli/explain_export.go) | `--json`のSession情報には境界オフセットがない。`--transcript --session-index`は生履歴を返すが、未取得Checkpointではfetch経路がある。このためMVPはGit Adapterで読み取りを完結 |
| [external-commands.md](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/docs/architecture/external-commands.md) | `entire timeline`はPATH上の`entire-timeline`を実行。引数・標準入出力を引き継ぎ、`ENTIRE_REPO_ROOT`を提供する。作業ディレクトリ自体は変えない |

## 責務分離

```text
GitStore.Select       Git範囲・トレーラー・保存済みの関連付けを解決
GitStore.Session      Checkpointメタデータ・生履歴の読み取りとチャンク再構成
Restore               累積性の検証、区間の和集合、Session単位の重複除去
decode                Agentごとの元イベント解析、未知情報の保持
Restore               元Session順序を保持した全Sessionの時系列統合
RenderText/JSONL      同じTimelineを人間向け／機械向けに出力
cli.Run               引数、環境変数、stdout/stderr、ファイル出力
```

各対象Checkpointの生履歴を確認するのは、そのCheckpointの区間の終端を正確に得るためである。短い累積履歴を順番に連結することはしない。選択された履歴同士を照合してから最長の履歴の必要な行だけを出力する。対象外Checkpointの開始位置や時刻から終端を推測しない。

重複除去のキーはSession IDと元の物理行位置である。UserのID欠落、同じ時刻・内容の別発言、同じIDを使った中間イベントを潰さない。履歴が途中で差し替えられた場合は位置の同一性を保証できないため、エラーにする。

すべての読み取りは解決済みGitオブジェクトIDに固定する。読み取り時は`GIT_OPTIONAL_LOCKS=0`と`GIT_NO_LAZY_FETCH=1`を指定する。ネットワーク取得、ref更新、設定変更、index更新は行わない。

## PLANの17ケースとテストの対応

| # | 要件 | 主な検証 |
| --- | --- | --- |
| 1 | 単一Checkpoint・単一Session | `TestRestoreCheckpointSessionMatrix/single` |
| 2 | 単一Checkpoint・複数Session | `TestRestoreCheckpointSessionMatrix/multiple_sessions` |
| 3 | 複数CheckpointをまたぐSession | `TestRestoreCheckpointSessionMatrix/cumulative` |
| 4 | 複数Sessionの時系列統合 | `TestMergeSessionsByTime` |
| 5 | 範囲以前からのSession | `TestRestoreCheckpointSessionMatrix/continues_before_range` |
| 6 | 対象外Checkpointを挟む区間 | `TestRestoreCheckpointSessionMatrix/noncontiguous`、`TestGitBranchCheckpointRestoration` |
| 7 | 重複防止 | `TestRestoreCheckpointSessionMatrix/overlapping`。関連Checkpoint一覧も保持 |
| 8 | Checkpointのないコミット | `TestGitNoCheckpointAndReadOnly`、Checkpointと通常コミットが混在する結合テスト |
| 9 | 時刻の同値・欠落 | `TestTimestampsAndIdentity`。時刻の逆行、全時刻不明のSessionも検証 |
| 10 | UserのID欠落 | 両Copilot Fixture、`TestTimestampsAndIdentity` |
| 11 | 複数content block | `TestTextAndJSONLRenderers`、`TestClaudeAndUnknownBlocksRemainVisible` |
| 12 | Markdown・コード・複数行 | `TestTextAndJSONLRenderers` |
| 13 | ツールの通常・詳細表示 | `TestTextAndJSONLRenderers`、`TestCLIOutputAndArguments` |
| 14 | 全文・長文を省略しない | `TestLongTextIsNeverTruncated`。長い日本語の複数行本文と狭い幅で検証 |
| 15 | JSONL妥当性・元データ保持 | `TestTextAndJSONLRenderers`で全行decodeと`raw`の往復一致。CLIではstdoutにJSON以外が混じらないことも検証 |
| 16 | `A..B` / `A...B` | `TestGitRangeSemantics`。両ブランチが進んだGit DAGで検証 |
| 17 | オフセットの不整合 | `TestInvalidRestorationFails`、`TestGitMissingCorruptAndChunkedTranscripts` |

追加でgit-refsのULID、トレーラーのない両方の関連付け、remote-tracking ref、独自ref、チャンク欠落、旧オフセット、圧縮版と異なるオフセット、未知ブロック、出力エラー、ファイル保護、CLI引数、実際のEntireプラグイン起動を検証する。

## 実行した検証

開発環境：macOS arm64、Go 1.23.1、Entire CLI 0.9.0。

```sh
go test -race -count=1 ./...
go vet ./...
make build
git diff --check
```

いずれも成功。`TestEntirePluginInvocation`もskipせず成功し、一時Gitリポジトリのサブディレクトリから実行した`entire timeline <range> --format jsonl`が直接実行と同じ結果になることを確認した。対応Agentの実際の対話セッションを新たに起動するテストは行っていない。Fixtureは個人の履歴を含まない合成データを使用している。

既知の制約と利用方法は[README](../README.md)を参照。
