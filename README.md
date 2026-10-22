# entire-timeline

EntireがGitに保存した会話を、指定したコミット範囲から復元するGo製CLIです。全Sessionの発言を1本の時系列ログに統合し、User・Assistantの本文、Markdown、コードブロック、中間メッセージを全文表示します。

Copilotの会話には発言ごとの要約を生成できます。要約は独立したGitブランチに保存し、EntireのCheckpoint同期先へ共有できます。

```sh
entire-timeline main...HEAD
entire timeline main...HEAD
```

## インストール

Go 1.23以降とGitが必要です。外部Goモジュールへの依存はありません。

```sh
go install github.com/ry023/entire-timeline/cmd/entire-timeline@latest
export PATH="$(go env GOPATH)/bin:$PATH"
```

このリポジトリからビルドする場合：

```sh
make build
export PATH="$PWD/bin:$PATH"
entire-timeline --help
entire timeline --help
```

Entireの[外部コマンド仕様](https://github.com/entireio/cli/blob/99d28955d5d24f589f5cd09d921a3356cae60e33/docs/architecture/external-commands.md)に従い、PATH上の`entire-timeline`が`entire timeline`として実行されます。プラグイン登録は不要です。`entire timeline`を使う場合は、外部コマンドに対応したEntire CLIもインストールしてください。検証にはEntire CLI 0.9.0を使用しています。直接実行ではEntire CLIは不要です。

## 使い方

Entireを利用しているGitリポジトリ内で実行します。

```sh
# ブランチの分岐点からHEADまでの会話
entire timeline main...HEAD

# Gitの通常の到達可能性による範囲
entire timeline main..HEAD

# ツールの引数・結果・元イベントも全文表示
entire timeline main...HEAD --include-tools

# 同じ復元結果をJSONLで出力
entire timeline main...HEAD --format jsonl

# ファイルへ保存
entire timeline main...HEAD --format jsonl --output timeline.jsonl

# スクロールしながら読む
entire timeline main...HEAD | less -R

# メタデータが独自のブランチに保存されている場合
entire timeline main...HEAD --checkpoint-ref refs/heads/my-checkpoints
```

オプションは範囲の前後どちらに置いても使えます。`--output -`はstdoutです。

| 範囲 | 解釈 |
| --- | --- |
| `A..B` | Bから到達でき、Aから到達できないコミット |
| `A...B` | `merge-base(A,B)..B`。Gitの対称差とは異なります |
| `..B` / `A..` | 省略した側はHEAD |

複数のmerge-baseがある場合は、曖昧な抽出を避けるためエラーにします。明示的な`A..B`を指定してください。

## 表示とエクスポート

各イベントに日時、短いSessionラベル、Role、Agentを表示します。冒頭にSessionラベルと元のSession IDの対応表が出ます。

通常表示でもツールの名前・引数の概要とTool Resultの存在がわかります。長いツール引数だけを短縮し、発言本文は省略しません。`--include-tools`ではツール詳細と元イベントのJSONを併記します。未知のイベントやcontent blockはJSONとして表示し、黙って捨てません。

本文は端末のソフトラップに任せ、改行・インデント・コードブロックを保持します。区切り線とツール概要の幅は`--width`、`COLUMNS`、80の優先順で決めます（区切り線は最大100桁）。ANSIカラーや自動ページャーは使いません。

JSONLは1行につき1元イベントです。ツール・システムイベントも含み、`--include-tools`によって復元結果は変わりません。

| フィールド | 内容 |
| --- | --- |
| `session_id` | 元のSession ID |
| `agent` | Agentの識別名（`copilot-cli` / `claude-code`） |
| `timestamp` | 元の時刻。欠落時は`null` |
| `type` / `role` | 元イベント種別と表示用Role |
| `event_id` | 元のID / UUID。存在する場合のみ |
| `source_line` | `full.jsonl`内の0始まりの物理行番号 |
| `checkpoints` | その行を対象区間に含むCheckpoint IDの配列 |
| `parts` | 表示用のテキスト・ツール・未知ブロック |
| `raw` | 元のイベント全体。未知フィールドも保持 |
| `summary` | 本文と一致する保存済み要約。存在する場合のみ |

`raw`はJSON値として保持します。空白やエスケープ表記のバイト単位の保存は保証しませんが、元イベントの情報を削除しません。IDがないUserイベントにも、衝突し得る合成IDを付けません。同じ内容やIDでも別の物理行なら保持します。

stdoutは表示結果専用です。JSONLモードではJSONL以外を出力しません。警告・エラーはstderrに出し、失敗時は終了コード1になります。全復元を検証してから出力を開始します。ファイル出力は同じディレクトリの一時ファイルから置換し、復元の失敗で既存出力を切り詰めません。新しいファイルの権限は`0600`です。

`parts[].kind`は会話本文の`text`とCoTの`reasoning`を区別します。会話本文だけを読む例：

```sh
entire timeline main...HEAD --format jsonl | jq -r 'select(.role == "user" or .role == "assistant") | {role, text: ([.parts[]? | select(.kind == "text") | .text] | join("\n\n"))} | select(.text != "") | "\(.role): \(.text)\n"'
```

## 発言ごとの要約

認証済みのGitHub Copilot CLI（`copilot`）が必要です。要約元のAgentと生成providerは、今回どちらも`copilot-cli`に対応しています。

```sh
# Entire側の設定を利用する（既に設定済みなら不要）
entire configure --summarize-provider copilot-cli

# 指定範囲を要約し、ローカルの専用ブランチにcommit
entire timeline summarize main...HEAD

# 保存済み要約と全文を併記
entire timeline main...HEAD

# 有効な既存要約も作り直す
entire timeline summarize main...HEAD --force
```

選択したコミット範囲のUser／Assistant本文を、Session・Role・時系列を保ってまとめて渡し、1発言につき日本語の1行要約を生成します。Tool呼び出し・結果・CoT・システムイベントは、生成対象にも文脈にも含めません。自然言語の中間応答も対象です。ストリーミングの`assistant.message_delta`は単独の発言として要約せず、完成した`assistant.message`を使います。元の会話・Tool・CoT・断片は復元結果と`raw`に残ります。

通常は有効な既存要約を再利用し、未要約・本文変更・プロンプト版変更のあった発言を生成します。provider/model設定の変更だけでは既存要約を無効にしません。必要なら`--force`を使ってください。生成対象外の範囲に保存済みの要約も保持します。要約生成後のpushは別操作です。

入力は既定128 KiB、要約対象は1回128発言までを目安に分割し、収まる前後の最大2発言ずつを文脈として重ねます。実際のモデル上限を自動検出するものではありません。`--batch-bytes N`で入力予算を変更できます。単独で予算を超える発言は全文を渡し、黙って切り捨てません。モデルが受け付けない場合はエラーになります。

不正・重複ID・欠落・途中切れを検証し、Copilotの異常終了やキャンセル時も、検証できた成功分をローカルに保存します。部分失敗は終了コード1です。同じコマンドを再実行すると不足分を生成します。生成中の進捗と結果件数はstderrへ出します。

参照する設定はEntireの`.entire/settings.json`と、未追跡の`.entire/settings.local.json`です。local側で上書きし、providerを切り替えてmodelを指定しない場合は前のmodelを引き継ぎません。Gitで追跡されたlocal設定はEntireと同様に無視します。

- `summary_generation.provider`：`copilot-cli`が必要です。未設定・未対応providerはエラーです。
- `summary_generation.model`：指定があればCopilotへ渡します。未指定時の実効モデル名は推測して保存しません。
- `summary_timeout_seconds`：正の値をCopilot呼び出しごとの制限秒数とします。未設定・0以下は自動タイムアウトなしです。Ctrl-Cでも中断できます。

timeline専用設定ファイルや、Entire設定への独自キーは追加しません。生成CLIは設定を読み取るだけです。

## 要約のGit保存とリモート同期

保存先は、コードと同じローカルGit内の固定ブランチ`entire-timeline/metadata/v1`です。コードの履歴と独立した最初のcommitを作り、ユーザーのHEAD・index・作業ツリーは変更しません。別cloneや常設の保存用ディレクトリは不要です。

確認のためにこのメタデータブランチをcheckoutしている場合は、別のブランチへ戻ってから生成・同期してください。他のworktreeでcheckoutされている場合も、HEADを動かさないよう更新を拒否します。

```text
summaries/copilot-cli/<session-id>.jsonl
```

1行が1発言のレコードです。Agent・Session・生履歴の物理行番号・本文SHA-256・要約・生成設定・生成日時を保持します。Session IDの危険な文字は可逆なpercent escapeとし、ハッシュ化やディレクトリ分散はしません。詳細は[第2段階の実装メモ](docs/IMPLEMENTATION_2.md)を参照してください。

```sh
# 保存済みデータをEntireと同じCheckpoint同期先へ共有
entire timeline remote sync

# checkoutせずにローカルの保存内容を確認
git ls-tree -r entire-timeline/metadata/v1
git show entire-timeline/metadata/v1:summaries/copilot-cli/SESSION_ID.jsonl
```

同期先はEntireの`strategy_options`（`checkpoint_push_remote`、`checkpoint_remote`、`push_sessions`）に従います。明示remote、Entireが記録したremote、`origin`、設定順のremoteの順に選びます。別リポジトリの設定も同じローカルGitからURLを直接指定して送受信します。`ENTIRE_CHECKPOINT_TOKEN`を使う別リポジトリ設定では、HTTPSとGitの認証ヘッダーを使います。

最初にpushを試し、履歴の分岐で拒否された場合はfetchし、ローカルcommitで変更したセッションファイルだけをリモート先頭に適用して再度pushします。別セッションの更新は保持し、同じセッションはローカルの**ファイル全体が後勝ち**です。発言単位のマージ・日時比較・force push・モデルによる再生成は行いません。ローカルに要約ブランチがない場合は、リモートから取得します。

`push_sessions: false`では手動同期もスキップします。同期は最大2分、競合の再試行は最大4回です。通信・認証エラーではローカルに保存した要約を残し、エラーを返します。

## push時の自動同期

```sh
entire timeline hooks install
entire timeline hooks status
entire timeline hooks uninstall
```

`pre-push`に同期用の区画を追加します。保存済み要約だけを同期し、hookから要約生成は始めません。既存のEntire／ユーザーのシェルhookとその入力を保持し、解除時には追加した区画だけを削除します。内部pushはhookを起動せず、再帰を防ぎます。選択した同期remote以外へのpushでは同期をスキップします。

同期の失敗は警告し、通常のコードpushを続けます。既存hook自身の失敗は従来どおりpushを止めます。`core.hooksPath`とworktreeに対応し、非シェルhookやsymlinkは自動編集しません。その場合はhook管理ツール側から`entire timeline remote sync`を呼び出してください。

登録時の実行ファイルの絶対パスを使用します。バイナリを移動した場合や、Entireの再インストールでhookが置き換わった場合は`hooks status`を確認し、`hooks install`を再実行してください。

## 復元の仕組みと確認したEntireの仕様

2026-10-21に公式ソースのコミット[`99d28955d5d24f589f5cd09d921a3356cae60e33`](https://github.com/entireio/cli/tree/99d28955d5d24f589f5cd09d921a3356cae60e33)を確認しました。調査の詳細とテスト対応表は[docs/IMPLEMENTATION.md](docs/IMPLEMENTATION.md)に記載しています。

1. Gitに範囲を解釈させ、`Entire-Checkpoint`トレーラーを列挙します。保存済みの`linked_commits[].sha`と`commit_sha`も照合します。`commit_sha`はインポート時のアンカーで、コードへの寄与を証明する情報ではありません。
2. `entire/checkpoints/v1`ブランチと`refs/entire/checkpoints/<末尾2文字>/<id>`を読みます。ローカルのv1ブランチがない場合は、取得済みのremote-tracking v1ブランチも探します。Sessionは配列内の位置ではなく`session_id`で識別します。
3. 生履歴`full.jsonl`とSessionメタデータを取得します。`checkpoint_transcript_start`は0始まりの生履歴の行オフセットです。Entireと同じ規則で古い`transcript_lines_at_start`にフォールバックします。省略された整数値は0として扱います。
4. 各Checkpointの担当区間は`[開始オフセット, そのCheckpointが保存した生履歴の行数)`です。選択したCheckpointの中で最長の累積履歴を使い、他の保存履歴がその接頭辞であることを検証します。担当区間の和集合だけを抽出するため、対象外Checkpointを挟んでも、その区間が混入しません。
5. Sessionと物理行番号によって重複を除き、時刻・Session ID・行番号で決定的に統合します。

`transcript.jsonl`は圧縮後の履歴で、`compact_transcript_start`も圧縮後の行番号です。旧バージョンにはCheckpoint単位の差分だけを保存する形式もあります。このため、生履歴の代用には使いません。

公式CLIの`entire checkpoint explain --json`には区間オフセットが含まれず、ローカルにデータがない場合にfetchする処理もあります。本ツールはGitの読み取りコマンドで必要なメタデータと生履歴を取得し、Entire内部のGoパッケージには依存しません。復元・表示はオンメモリかつ読み取り専用で、モデル呼び出しやネットワーク通信は行いません。明示的な`summarize`・`remote sync`・`hooks install/uninstall`が、それぞれ要約履歴の保存・送受信・hookの編集を行います。

## 対応範囲と制約

- 最初の重点対象はCopilot CLIです。ネイティブの`user.message` / `assistant.message` / `tool.execution_*`形式と、提示されたEntire compact v1形式に対応しています。Claude CodeのJSONLにも対応します。他Agentはオフセットの意味を保証できないため、明示的にエラーにします。
- ローカルにあるGitバックエンドのCheckpointを対象にします。未取得の履歴、APIのみの保存先、独自バックエンドは自動取得しません。必要なデータはあらかじめEntireの利用環境に従って取得してください。独自メタデータrefは`--checkpoint-ref`で追加できます。
- 対象Checkpointの生履歴がない、行オフセットが範囲外、履歴が書き換わって接頭辞一致しない、同じCheckpointの保存先同士で内容が異なる、JSONが壊れている場合はエラーにします。全Session履歴への置き換えは行いません。
- 生履歴が`full.jsonl`と連番の`.001`、`.002`などに分割されている場合は再構成します。途中のチャンク欠落もエラーにします。古い`full.log`のみの保存形式は未対応です。
- タイムスタンプ欠落時は同Sessionの直前の時刻、先頭なら次の既知時刻を整列の基準に使います。全時刻が不明のSessionは最後に置きます。時刻が逆行する場合はSession内の元の順序を優先します。どちらもstderrに警告し、JSONLの元の時刻を補完・変更しません。不正な日時文字列はエラーです。
- チャンクやイベントを含む選択済みの累積履歴をメモリに読みます。巨大な履歴ではその量に応じたメモリが必要です。
- Session配列にある会話を対象とします。`tasks/`に別ファイルとして保存されるサブエージェント専用履歴の追加復元は対象外です。親履歴内に記録されたサブエージェント関連イベントは保持します。
- 正確さはEntireに保存された履歴と境界情報に依存します。保存前に失われたイベントや、Entireが秘匿化した値は復元できません。
- 要約の同期はGitバックエンドを対象にし、HTTP/APIの独自バックエンド（`entire://`等）は未対応です。別リポジトリURLの導出はSSH/SCP/HTTPSに対応します。複数push URLへの同時送信は未対応のため、明示的なエラーにします。

## 開発と検証

```sh
make test
make vet
make build
```

Fixtureテストと一時Gitリポジトリの結合テストで、[PLANのテスト要件](docs/PLAN.md)と[第2段階の要件](docs/PLAN_2.md)を検証します。要約はFake Copilot、同期はローカルbareリポジトリを使い、実際の`git push`からのhook起動も検証します。主要テストはEntire CLI・モデル認証・ネットワークなしで実行できます。Entire CLIがインストールされていれば、実際の`entire timeline`呼び出しと直接実行のJSONL一致も検証します。未インストールなら、その追加テストだけskipします。
