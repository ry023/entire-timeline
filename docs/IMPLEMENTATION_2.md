# 第2段階MVPの実装メモ

実装日：2026-10-23。[PLAN_2.md](PLAN_2.md)の要約生成・Git保存・同期・hook管理を実装しました。元の[PLAN.md](PLAN.md)は変更していません。

## コマンドと対象

```sh
entire timeline summarize main...HEAD
entire timeline main...HEAD
entire timeline remote sync
entire timeline hooks install
entire timeline hooks status
entire timeline hooks uninstall
```

直接実行の`entire-timeline`でも同じです。追加のGo依存はありません。生成元・生成providerはCopilot CLIのみです。既存のClaude Codeの復元・表示は維持します。

## 計画時の未確定事項に対する実装

| 項目 | 今回の実装 |
| --- | --- |
| 生成CLI | `summarize <range>`。`--force`と`--batch-bytes`、既存の`--checkpoint-ref`を利用可能 |
| 表示 | 本文の前に`Summary:`を併記。JSONLは有効な保存済み要約があるイベントに`summary`を追加 |
| 入力単位 | 選択範囲の会話本文を時系列順にまとめる。各メッセージに要求ID・Session・Role・生成要否を付与 |
| 中間応答 | 完成した自然言語の中間応答も対象。`assistant.message_delta`は対象・文脈から除き、完成イベントを使う |
| CoT／Tool | ネイティブ`reasoningText`・compactのthinking blockを`reasoning`に分離。要約には`text`だけを使う |
| 大きい会話 | 既定128 KiB、最大128対象／回。分割時は入力予算の一部を文脈に空け、収まる前後最大2発言を重ねる |
| 巨大な1発言 | 本文は切り捨てず、1件をそのまま試す。providerに拒否された場合は失敗として扱う |
| provider未設定 | エラー。Entire側の`entire configure --summarize-provider copilot-cli`を案内 |
| model未設定 | Copilotに任せる。取得できない実効model名を捏造しない |
| タイムアウト | `summary_timeout_seconds`の正の値を1回のCLI呼び出しに適用。未設定・0以下は無制限 |
| 再利用 | Agent・Session・物理行・本文SHA-256・プロンプト版が一致するものを再利用。provider/modelの変更時は必要に応じ`--force` |
| 応答検証 | JSONLの要求IDと要約を検証。未知ID・重複ID・空要約・改行入り要約・不正JSONを除外 |
| 部分失敗 | 各呼び出しで検証済みの行を集め、処理終了時に成功分を保存。失敗は終了コード1、再実行は不足分を生成 |
| キャンセル | Copilotを停止し、取得済みの成功分を別の30秒保存用contextでcommit。プロセスの強制終了には対応しない |
| hook失敗 | 同期失敗を警告しコードpushを継続。既存hookの終了コード・引数・stdinを保持 |
| hook再設定 | Entireがhookを置き換えると未登録状態になる。`status`で確認し`install`を再実行 |

入力予算はモデルの実際のtoken／出力上限を自動検出するものではありません。会話が収まれば一括生成し、分割時も発言ごとのCLI呼び出しにはしません。元データでCoTと本文が区別されない場合、本文の意味から推測して削除しません。

## データとGit操作

固定refは`refs/heads/entire-timeline/metadata/v1`、保存パスは`summaries/<agent>/<session-id>.jsonl`です。危険なIDのUTF-8バイトをpercent escapeします（`%`・`.`・パス区切りも含む）。UUIDの英数字とハイフン等は読みやすいまま残ります。

```json
{"schema_version":1,"session_id":"session-xxx","agent":"copilot-cli","source_line":42,"event_id":"event-xxx","text_sha256":"本文のSHA-256の64桁hex","summary":"認証機能の実装を依頼。","generation":{"provider":"copilot-cli","model":"指定した場合のみ","prompt_version":1},"created_at":"2026-10-23T00:00:00Z"}
```

`source_line`は生Transcriptの0始まりの物理行です。元IDがなければ`event_id`を省略します。複数text blockは元順序で`\n\n`連結し、その本文のUTF-8バイト列をハッシュ化します。Tool／CoTの変更だけでは本文ハッシュは変わりません。

保存時はセッションの既存行を読み、生成できた行だけを更新します。範囲外・今回失敗した発言の既存行も保持します。不正な既存ファイルはエラーにし、上書きして消しません。

`hash-object`・私有の一時index・`write-tree`・`commit-tree`でコードから独立した履歴を作ります。`update-ref`は旧値を指定して更新し、同時ローカル保存に失敗したら読み直して最大5回試します。ユーザーのindex・作業ツリーをcheckout/stashしません。一時indexは処理後に削除します。通常のGit commitと同じくGitのユーザー名・メール設定が必要です。

固定メタデータブランチをどれかのworktreeでcheckoutしている場合は、生成結果の保存・同期を拒否します。`update-ref`によって、そのworktreeのHEADだけがindex／作業ツリーを残して移動することを防ぎます。

## Entire設定との互換範囲

EntireのGoパッケージには依存せず、必要な設定だけを標準JSONパーサーで読みます。他のキーは許容し、本家設定に書き込みません。

- リポジトリルートの`.entire/settings.json` → 未追跡の`.entire/settings.local.json`の順に上書き。
- provider/modelはフィールド単位で統合。provider切替時にmodelを指定しなければ旧modelを破棄。
- `strategy_options.checkpoint_push_remote` → common Git dirの`entire-checkpoint-sync-remotes.json` → `origin` → config順のremoteで選択。
- `strategy_options.checkpoint_remote`があれば、選択したremoteの転送方式・ホストから別リポジトリURLを導出。別所有者から継承された共有設定は本家同様に無視し、local設定の明示値は優先。
- `ENTIRE_CHECKPOINT_TOKEN`を使う別リポジトリ設定ではHTTPS化し、Gitの環境設定経由でAuthorizationヘッダーを渡す。tokenをURL・argvに追加しない。
- `strategy_options.push_sessions: false`では自動・手動同期ともスキップ。

SSH/SCP/HTTPSのGitリモートを対象とします。複数push URLの同時送信、`entire://`等の独自APIバックエンドには対応していません。

Copilot呼び出しは本家のテキスト生成を参考に、一時ディレクトリをcwdにし、プロンプトをstdinで渡します。Git／Entireのリポジトリ用環境変数と権限の広いCopilot環境指定を除き、組み込みMCP・独自指示・各種ツールを無効化します。Copilotの通常のログイン設定を使い、独自の認証ファイルは作りません。

## 同期とhook

同期は2分以内、最大4回です。ローカルcommitをpushし、non-fast-forwardの拒否だけを対象にfetch・再適用・再pushします。認証や通信の失敗を競合として扱いません。初回取得では一時refへfetchし、ローカルrefを更新します。`FETCH_HEAD`とremote設定は変更しません。

共通祖先以降にローカルで触ったパスを集め、各パスの最終ローカルblobをリモート先頭に適用する1つのcommitを作ります。途中で元の内容に戻したファイルも対象です。独立した初回履歴同士ならローカルで触った全ファイルを対象とし、リモートの他ファイルは維持します。同一セッションファイルの発言単位のマージはしません。リモートの履歴を祖先として保ち、通常のpushで更新します。

`pre-push`はシェルスクリプトに限って、shebangの後ろへ開始・終了マーカー付きの区画を追加します。標準入力を消費せず、既存hookやEntireの`.pre-entire`連結を保持します。専用wrapperへ既存hookを退避しないため、Entireがhookを再設定しても相互呼び出しの再帰を作りません。自分で作成したhookは解除時に削除し、既存hookなら追加区画だけを削除します。Gitにhookのパスを解決させ、worktree・`core.hooksPath`に対応します。

内部のmetadata pushでは空の`core.hooksPath`を使います。同期は登録した実行ファイルを絶対パスで呼び出します。通常のpushが選択remoteと異なる場合は同期をスキップします。非シェルhook・symlink・壊れた区画は編集せず診断します。

## 検証

```sh
make test
make vet
make build
git diff --check
```

| テスト | 検証内容 |
| --- | --- |
| `internal/timeline/render_test.go` | native／compactの本文・CoT区別、元イベント保持、要約併記、stream断片の扱い |
| `internal/summary/summary_test.go` | 一括入力、前後文脈、応答の順序差・部分失敗、再開、全文保持、設定統合、ローカル同時保存、HEAD/index不変 |
| `internal/summary/sync_test.go` | bare remoteで初回push/fetch・分岐復旧・独立履歴・別Session保持・同一Session後勝ち・remote選択・再帰防止 |
| `internal/summary/hooks_test.go` | 既存hookとstdin保持、終了コード、同期失敗時の継続、再登録・解除、Entireによる置換、worktree |
| `internal/cli/summarize_test.go` | Fake Copilot実行、argv/cwd/環境・入力の検証、保存・表示・再利用・再生成・部分失敗・キャンセル |
| `internal/cli/remote_test.go` | 実バイナリのhook登録→実際のGit push→bare remoteへの要約同期→解除→初回取得 |
| `internal/cli/run_test.go` | 既存CLI、Entire外部コマンドとの一致 |

モデルによる実際の生成や外部サービスへのpushはテストでは行いません。生成はFake Copilot、同期は一時bareリポジトリで検証します。Entire CLIがなければ外部コマンドの追加smoke testだけskipします。本家ソースの参照先は[PLAN_2末尾](PLAN_2.md#12-本家調査の参照先)に記載しています。
