# entire-timeline

EntireがGitに保存した会話を、指定したコミット範囲から復元するGo製CLIです。全Sessionの発言を1本の時系列ログに統合し、User・Assistantの本文、Markdown、コードブロック、中間メッセージを全文表示します。

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

`raw`はJSON値として保持します。空白やエスケープ表記のバイト単位の保存は保証しませんが、元イベントの情報を削除しません。IDがないUserイベントにも、衝突し得る合成IDを付けません。同じ内容やIDでも別の物理行なら保持します。

stdoutは表示結果専用です。JSONLモードではJSONL以外を出力しません。警告・エラーはstderrに出し、失敗時は終了コード1になります。全復元を検証してから出力を開始します。ファイル出力は同じディレクトリの一時ファイルから置換し、復元の失敗で既存出力を切り詰めません。新しいファイルの権限は`0600`です。

## 復元の仕組みと確認したEntireの仕様

2026-10-21に公式ソースのコミット[`99d28955d5d24f589f5cd09d921a3356cae60e33`](https://github.com/entireio/cli/tree/99d28955d5d24f589f5cd09d921a3356cae60e33)を確認しました。調査の詳細とテスト対応表は[docs/IMPLEMENTATION.md](docs/IMPLEMENTATION.md)に記載しています。

1. Gitに範囲を解釈させ、`Entire-Checkpoint`トレーラーを列挙します。保存済みの`linked_commits[].sha`と`commit_sha`も照合します。`commit_sha`はインポート時のアンカーで、コードへの寄与を証明する情報ではありません。
2. `entire/checkpoints/v1`ブランチと`refs/entire/checkpoints/<末尾2文字>/<id>`を読みます。ローカルのv1ブランチがない場合は、取得済みのremote-tracking v1ブランチも探します。Sessionは配列内の位置ではなく`session_id`で識別します。
3. 生履歴`full.jsonl`とSessionメタデータを取得します。`checkpoint_transcript_start`は0始まりの生履歴の行オフセットです。Entireと同じ規則で古い`transcript_lines_at_start`にフォールバックします。省略された整数値は0として扱います。
4. 各Checkpointの担当区間は`[開始オフセット, そのCheckpointが保存した生履歴の行数)`です。選択したCheckpointの中で最長の累積履歴を使い、他の保存履歴がその接頭辞であることを検証します。担当区間の和集合だけを抽出するため、対象外Checkpointを挟んでも、その区間が混入しません。
5. Sessionと物理行番号によって重複を除き、時刻・Session ID・行番号で決定的に統合します。

`transcript.jsonl`は圧縮後の履歴で、`compact_transcript_start`も圧縮後の行番号です。旧バージョンにはCheckpoint単位の差分だけを保存する形式もあります。このため、生履歴の代用には使いません。

公式CLIの`entire checkpoint explain --json`には区間オフセットが含まれず、ローカルにデータがない場合にfetchする処理もあります。本ツールはGitの読み取りコマンドで必要なメタデータと生履歴を取得し、Entire内部のGoパッケージには依存しません。復元はオンメモリで、DB、永続キャッシュ、LLM APIは使いません。実行時にGitのref・index・設定・作業ツリーを変更しません。

## 対応範囲と制約

- 最初の重点対象はCopilot CLIです。ネイティブの`user.message` / `assistant.message` / `tool.execution_*`形式と、提示されたEntire compact v1形式に対応しています。Claude CodeのJSONLにも対応します。他Agentはオフセットの意味を保証できないため、明示的にエラーにします。
- ローカルにあるGitバックエンドのCheckpointを対象にします。未取得の履歴、APIのみの保存先、独自バックエンドは自動取得しません。必要なデータはあらかじめEntireの利用環境に従って取得してください。独自メタデータrefは`--checkpoint-ref`で追加できます。
- 対象Checkpointの生履歴がない、行オフセットが範囲外、履歴が書き換わって接頭辞一致しない、同じCheckpointの保存先同士で内容が異なる、JSONが壊れている場合はエラーにします。全Session履歴への置き換えは行いません。
- 生履歴が`full.jsonl`と連番の`.001`、`.002`などに分割されている場合は再構成します。途中のチャンク欠落もエラーにします。古い`full.log`のみの保存形式は未対応です。
- タイムスタンプ欠落時は同Sessionの直前の時刻、先頭なら次の既知時刻を整列の基準に使います。全時刻が不明のSessionは最後に置きます。時刻が逆行する場合はSession内の元の順序を優先します。どちらもstderrに警告し、JSONLの元の時刻を補完・変更しません。不正な日時文字列はエラーです。
- チャンクやイベントを含む選択済みの累積履歴をメモリに読みます。巨大な履歴ではその量に応じたメモリが必要です。
- Session配列にある会話を対象とします。`tasks/`に別ファイルとして保存されるサブエージェント専用履歴の追加復元は対象外です。親履歴内に記録されたサブエージェント関連イベントは保持します。
- 正確さはEntireに保存された履歴と境界情報に依存します。保存前に失われたイベントや、Entireが秘匿化した値は復元できません。

## 開発と検証

```sh
make test
make vet
make build
```

Fixtureテストと一時Gitリポジトリの結合テストで、[PLANのテスト要件](docs/PLAN.md)を検証します。主要テストはEntire CLIやネットワークなしで実行できます。Entire CLIがインストールされていれば、実際の`entire timeline`呼び出しと直接実行のJSONL一致も検証します。未インストールなら、その追加テストだけskipします。
