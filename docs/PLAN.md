# 開発依頼：Entireの会話履歴を復元・表示するGo製CLIプラグイン

## 1. 目的

Entire（https://github.com/entireio/cli）に保存されたAI Coding Agentの会話履歴を、指定されたGitコミット範囲から復元し、全Sessionを横断した時系列チャットログとしてターミナルに表示するツールをGoで開発してください。

ツール名は `entire-timeline` とします。

Entireの外部コマンドプラグイン機構を利用し、以下の両方で動作できるようにします。

```bash
entire-timeline main...HEAD
entire timeline main...HEAD
```

**MVPのゴールは、指定されたコミット範囲に対応する全会話を、ターミナル上で人間が読める状態まで復元することです。**

単にJSONを取得・出力するだけではなく、会話の内容を読み返せるところまで実装してください。

## 2. 基本方針

- 実装言語はGo。
- Entireを会話履歴のSingle Source of Truthとする。
- Entireのデータは読み取り専用で扱う。
- Entire本体の改造やフォークは行わない。
- Entire CLIおよびGitコマンドの利用を優先する。
- Entireの内部Goパッケージへの直接依存は、必要な理由がない限り避ける。
- 会話の復元と時系列統合はオンメモリで行う。
- 独自DBや永続キャッシュは不要。
- 外部LLM APIは利用しない。
- TUIやWeb UIは今回実装しない。

将来的には会話の1行要約、タグ付け、メタデータ永続化、TUIなどを追加したいと考えています。

ただし、今回それらを先行実装する必要はありません。

## 3. Entireの構造と調査事項

EntireのCheckpointとSessionは多対多の関係を持ちます。

- 1つのCheckpointに複数Sessionが含まれ得る。
- 1つのSessionが複数Checkpointにまたがり得る。
- CheckpointにはSessionのTranscriptが保存される。
- Session IDを使ってCheckpointをまたいだ同一Sessionを識別できる。
- CheckpointごとにTranscript上の開始位置を表すメタデータが存在する。

特に以下のフィールドを調査してください。

- `session_id`
- `checkpoint_transcript_start`
- `compact_transcript_start`

参考リポジトリ：

https://github.com/entireio/cli

参考ドキュメント：

- `docs/architecture/sessions-and-checkpoints.md`
- `docs/architecture/external-commands.md`

Entireの仕様は更新され得るため、実装前に現行ソースコードを確認してください。

CLIの引数、Checkpointの保存形式、オフセットの単位などは推測で決めないでください。

## 4. 会話復元アルゴリズム

基本的には、以下のアルゴリズムで実装してください。

### Step 1：対象Checkpointの列挙

指定されたGitコミット範囲に関連付けられたCheckpointを列挙します。

```bash
entire-timeline main...HEAD
```

Git revision rangeの解釈：

- `A..B`：通常のGitコミット範囲
- `A...B`：`merge-base(A,B)..B` として扱う

Entireの `Entire-Checkpoint` トレーラーや、コミットとCheckpointの関連付け情報を使います。

トレーラー以外の関連付け方法も考慮してください。

### Step 2：Sessionの列挙

各Checkpointに含まれるSessionを取得します。

概念的には以下の情報が必要です。

```text
Checkpoint A
  Session X
  Session Y

Checkpoint B
  Session X
  Session Z

Checkpoint C
  Session X
  Session Y
```

Checkpoint内のSession Indexは位置情報であり、同一Sessionの識別には `session_id` を使用します。

### Step 3：Sessionごとに最後のCheckpointを取得

同一Sessionについて、対象範囲に含まれる最後のCheckpointを特定します。

EntireのCheckpointが累積Transcriptを保持している場合、最後のCheckpointのTranscriptを取得すれば、それまでの会話全文が得られます。

この性質を利用し、Checkpointごとに重複した会話を連結することは避けてください。

ただし、累積Transcriptの仕様と前提条件は実装ソースで確認してください。

### Step 4：対象コミット範囲の会話を切り出す

各Checkpointが担当する会話区間をメタデータから取得します。

例えば、

```text
Session X:

Checkpoint A : [0, 10)   対象
Checkpoint B : [10, 20)  対象外
Checkpoint C : [20, 30)  対象
```

なら、抽出するのは `[0,10)` と `[20,30)` です。

Checkpointの開始位置と終了位置の算出方法は、Entireの現行実装に従ってください。

`full.jsonl` と `transcript.jsonl` では位置情報の基準が異なる可能性があります。

原則として、生Transcriptと `checkpoint_transcript_start` を使う方針ですが、Agentによって解釈が異なる場合は専用処理を設けてください。

**会話区間を正確に切り出すことが最優先です。**

範囲情報が不完全で正しい抽出を保証できない場合は、勝手に全履歴を代用せず、明確なエラーにしてください。

### Step 5：全Sessionの会話を統合

対象区間から取り出したイベントを時系列で統合します。

- Sessionごとにまとめて表示しない。
- 全Sessionのイベントを混在させ、時刻順に表示する。
- 各イベントがどのSession由来か識別できるようにする。
- 同一Session内のイベント順序を維持する。
- タイムスタンプが同じ場合は決定的な順序を採用する。
- タイムスタンプが欠落している場合もイベントを無言で破棄しない。
- 複数Checkpointの範囲が重なる場合は、同一イベントの二重出力を避ける。

## 5. 人間向けのターミナル表示【MVP必須】

**通常実行では、人間が読めるチャットログをstdoutに出力してください。**

例えば以下の表示です。

```text
$ entire timeline main...HEAD

Entire Timeline
Range: main...HEAD
Checkpoints: 5
Sessions: 3

────────────────────────────────────────

[10:00:12] [Session A] USER

認証機能を実装してほしい。
既存のAuthProviderを利用すること。

────────────────────────────────────────

[10:00:18] [Session A] ASSISTANT

既存の認証処理を調査します。

AuthProviderを拡張する方針で進めます。

  ↳ Tools: Read × 5, Edit × 2

────────────────────────────────────────

[10:01:30] [Session B] USER

並行して認証処理のテストを書いて。

────────────────────────────────────────

[10:02:41] [Session A] ASSISTANT

実装が完了しました。

変更点：
- AuthProviderの拡張
- エラーハンドリングの追加

────────────────────────────────────────

[10:03:12] [Session B] ASSISTANT

テストを追加しました。
```

これは表示イメージであり、厳密なレイアウトは任せます。

ただし、次の要件を満たしてください。

### 5.1 会話全文の表示

UserおよびAssistantの発言本文をすべて表示してください。

- 1行要約しない。
- 長文を勝手に省略しない。
- Markdown本文を保持する。
- 改行やコードブロックを保持する。
- 複数のcontent blockが存在する場合も、テキストを欠落させない。
- Assistantの中間メッセージも、取得できる範囲で保持する。

通常のstdout出力なので、端末のスクロールバックや `less` で読めれば十分です。

### 5.2 Sessionの識別

各発言にSessionの識別情報を付与してください。

Session IDが長い場合は、`Session A`、`Session B` などの短いラベルを割り当てても構いません。

ただし、元のSession IDとの対応は確認できるようにしてください。

### 5.3 Tool Callの扱い

Tool CallやTool Resultも、会話の流れを理解するために重要です。

ただし、すべてのTool Resultを常時全文表示すると、チャットが読みにくくなる可能性があります。

MVPでは以下の仕様とします。

**通常表示：**

- User/Assistantの自然言語メッセージは全文表示。
- Tool Callは実行内容がわかる程度に簡潔に表示。
- 大量のTool Resultは初期状態では全文表示しなくてよい。
- Tool Callの存在自体は無言で省略しない。

**詳細表示：**

```bash
entire-timeline main...HEAD --include-tools
```

このオプションではTool CallとTool Resultの情報を可能な限り全文出力してください。

したがって、通常表示と詳細表示の切り替えはレンダリング段階で行い、復元処理自体はすべてのイベントを保持する設計にしてください。

### 5.4 表示品質

- メッセージ間に視覚的な区切りを設ける。
- 時刻・Session・Roleを識別できる。
- ターミナル幅を考慮する。
- ANSIカラーは任意。使う場合は非TTY時に無効化できること。
- 出力はstdout、診断ログはstderrに分離する。
- 対話的なUI操作は不要。
- ページャーの自動起動も不要。

## 6. JSONLエクスポート【MVP必須】

将来のプログラム連携のため、JSONLエクスポートも実装してください。

```bash
entire-timeline main...HEAD --format jsonl
```

JSONLには、可能な限りすべてのイベントを含めてください。

最低限必要な情報：

- Session ID
- Agent種別
- Timestamp
- Event Type / Role
- 元のEvent ID（存在する場合）
- 元のイベント内容

元データを破棄するような不可逆な正規化は避けてください。

JSONLモードではstdoutにJSONL以外の文字列を出さないでください。

また、以下もサポートしてください。

```bash
entire-timeline main...HEAD --output timeline.jsonl --format jsonl
```

内部的には、

```text
Entire / Git
    ↓
Checkpoint解決
    ↓
Session復元
    ↓
会話区間抽出
    ↓
時系列統合
    ↓
TimelineEvent[]
    ├── Text Renderer → stdout
    └── JSONL Renderer → stdout / file
```

という責務分離を想定しています。

Text RendererとJSONL Rendererで、CheckpointやSessionの復元処理を重複実装しないでください。

## 7. Agent形式への対応

最初の重点対象はGitHub Copilot CLIです。

Entireに保存されたCopilot CLIのJSONLの例：

```jsonl
{"v":1,"agent":"copilot-cli","cli_version":"0.9.0","type":"assistant","ts":"2026-08-21T08:59:13.919Z","id":"ab265d46-654a-4e59-89fd-56c6df02f086","output_tokens":690,"content":[{"text":"実装が完了しました","type":"text"}]}
{"v":1,"agent":"copilot-cli","cli_version":"0.9.0","type":"user","ts":"2026-08-21T08:59:49.732Z","content":[{"text":"確認してください","type":"text"}]}
```

注意点：

- Assistantには `id` がある場合がある。
- Userには `id` がない場合がある。
- `ts` は両方に存在する。
- `content` は配列であり、複数種類のブロックが含まれ得る。

これは実際に取得されたTranscriptのサンプルです。ただし、この形式をすべてのAgentやEntireの全保存形式に共通するものと決めつけないでください。

元イベントの安定したIDがあれば保持してください。

IDがない場合、`session_id + type + ts` などを暫定識別子として使うことは可能ですが、一意性を保証できない場合があります。

今回、永続メッセージIDの完全な管理基盤を作る必要はありません。

将来のAnnotation機能に備え、元のイベント情報を失わないことを優先してください。

他Agentについては、共通の復元処理から形式依存部分を分離し、未対応のデータを無言で欠落させないようにしてください。

## 8. CLIインターフェース

最低限、以下を実装してください。

```bash
# 人間向けチャットログ（デフォルト）
entire-timeline main...HEAD

# 同上、Entireプラグイン経由
entire timeline main...HEAD

# 通常のGit revision range
entire-timeline main..HEAD

# Toolの詳細も表示
entire-timeline main...HEAD --include-tools

# JSONL出力
entire-timeline main...HEAD --format jsonl

# ファイル出力
entire-timeline main...HEAD --format jsonl --output timeline.jsonl

# ヘルプ
entire-timeline --help
```

プラグインの配置・インストール方法はEntireの公式仕様に従ってください。

`ENTIRE_REPO_ROOT` などの環境変数も必要に応じて利用してください。

## 9. テスト要件

次のケースをテストしてください。

1. 単一Checkpoint・単一Session
2. 単一Checkpoint・複数Session
3. 複数CheckpointにまたがるSession
4. 複数Sessionの時系列統合
5. コミット範囲以前から継続するSession
6. 対象外Checkpointを挟む非連続区間
7. Checkpoint間のイベント重複防止
8. Checkpointのないコミット
9. タイムスタンプの同値・欠落
10. UserメッセージにIDが存在しないCopilot CLIデータ
11. 複数content blockを含むイベント
12. Markdown・コードブロック・複数行メッセージ
13. Tool CallとTool Resultの通常表示・詳細表示
14. Text Rendererの全文表示（省略がないこと）
15. JSONL Rendererの出力妥当性
16. `A..B` と `A...B` の範囲解決
17. EntireのメタデータとTranscriptのオフセットが不整合なケース

テストはFixtureを使い、Entire CLIの実環境がなくても主要な復元ロジックと表示ロジックを検証できるようにしてください。

## 10. 実装手順

1. 作業ディレクトリ・既存構成を確認。
2. Entireの公式ソース・CLI仕様を調査。
3. Checkpointの会話区間の扱いを確認。
4. GitおよびEntire CLI Adapterを実装。
5. Session復元・区間抽出ロジックを実装。
6. 全Sessionの時系列統合を実装。
7. Text Rendererを実装。
8. JSONL Rendererを実装。
9. CLIとEntireプラグイン連携を実装。
10. テストを実装・実行。
11. READMEにインストール方法と使用例を記載。

実装上の細かな判断は合理的に進めて構いません。

ただし、Entireの実装依存仕様については現行ソースで確認してください。

## 11. 完成条件

次のコマンドを実行したとき、

```bash
entire timeline main...HEAD
```

**指定されたコミット範囲に関係する全Sessionの会話が、正確な範囲で復元され、1本の時系列チャットログとしてターミナルに表示されること。**

UserとAssistantの発言は全文を読める状態とし、Tool Callについても実行の流れを確認できるようにします。

また、

```bash
entire timeline main...HEAD --format jsonl
```

で同じ復元結果を機械処理可能な形式で取得できること。

今回はここまでをMVPとします。

TUI、GUI、AIによる要約、タグ、永続メタデータ管理は対象外です。

過度な抽象化や将来機能の先行実装を避け、まず使えるCLIツールとして完成させてください。

実装後は、テスト結果、利用方法、確認したEntireの仕様、既知の制約を報告してください。
