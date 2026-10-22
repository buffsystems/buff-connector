<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo.svg" alt="Buff Development" width="420">
  </picture>
</p>

<p align="center">
  <b>Коннектор Buff — агент в вашем периметре.</b><br>
  ИИ-агенты Buff работают с вашим кодом, трекерами и документами, а контент, ключи и интеллектуальная собственность остаются у вас.
</p>

<p align="center">
  <a href="https://github.com/buffsystems/buff-connector/releases"><img alt="Выпуск" src="https://img.shields.io/github/v/release/buffsystems/buff-connector?label=%D0%B2%D1%8B%D0%BF%D1%83%D1%81%D0%BA&color=2563eb"></a>
  <a href="https://github.com/buffsystems/buff-connector/pkgs/container/buff-connector"><img alt="Образ" src="https://img.shields.io/badge/ghcr.io-buff--connector-2563eb?logo=docker&logoColor=white"></a>
  <a href="LICENSE"><img alt="Лицензия" src="https://img.shields.io/badge/лицензия-MIT-64748b"></a>
  <a href="https://buff.systems/docs/connector"><img alt="Документация" src="https://img.shields.io/badge/документация-buff.systems-0b0d12"></a>
</p>

<p align="center">
  <a href="#зачем-он-нужен">Зачем</a> ·
  <a href="#что-умеет">Что умеет</a> ·
  <a href="#установка">Установка</a> ·
  <a href="#свои-интеграции-в-периметре">Свои интеграции</a> ·
  <a href="#как-это-работает">Как это работает</a> ·
  <a href="https://buff.systems/docs/connector">Документация</a> ·
  <a href="README.en.md">English</a>
</p>

---

**Buff** превращает задачу в трекере в готовую ветку с тестами, отвечает на вопросы по документам компании и пишет глубокие исследования — силами ИИ-агентов. Чтобы работать, агентам нужны ваши репозитории, трекеры и база знаний. **Коннектор** даёт им этот доступ, не вынося из вашей сети то, что должно в ней остаться: он стоит у вас, сам ходит к вашим системам изнутри, а наружу — только исходящее соединение и ровно то, что нужно для ответа.

Коннектор один во всех изданиях Buff — **Community, Enterprise on-premise и SaaS**. Его код открыт: прежде чем впустить агента в свою сеть, его можно прочитать целиком.

## Зачем он нужен

| | |
|---|---|
| **Ключи не покидают периметр** | Токены git-провайдеров, трекеров и wiki, ключ самого агента — только на машине коннектора. Даже интеграции их не видят: запрос с учётными данными собирает ядро коннектора и подставляет их само. |
| **Контент — по запросу и только на чтение** | Модель получает только инструменты, помеченные как читающие; писать в ваши системы она не может. Ответ в тикет пишет сам коннектор по команде платформы — не модель. |
| **Внутрь ничего не открывается** | Только исходящие соединения: к вам в сеть никто не подключается, события трекеров приходят опросом — порт наружу не нужен (исключения — отдельные вебхуки, [подробнее](https://buff.systems/docs/connector/trackers#события-опрос-и-вебхуки)). |
| **Свои интеграции без передачи интеллектуальной собственности** | Внутренние системы подключаются вашим сервером MCP или плагином на SDK. Код работает на машине коннектора и никуда не уходит — модель видит только описание инструментов и результаты вызовов. |
| **Закрытые git-серверы** | Git-мост переносит репозитории, недоступные из интернета, и возвращает обратно pull request'ы — без проброса вашего git наружу. |
| **Buff Code через компанию** | Шлюз разработчика: [Buff Code](https://github.com/buffsystems/buff-code) инженеров работает с моделями, знаниями и подключениями организации через коннектор — без ключей провайдеров на машинах разработчиков. |

## Издания

| | Где стоит коннектор | Что это даёт |
|---|---|---|
| **Community** | встроен в установку на вашей машине | git-провайдеры и источники знаний — с токенами в файле рядом с установкой |
| **Enterprise on-premise** | в вашем контуре, рядом с установкой | все системы контура — без выхода данных за его границу |
| **SaaS** | в вашей сети | облачный Buff работает с вашими системами, а ключи и доступ в сеть остаются у вас |

## Что умеет

| | |
|---|---|
| **Git-мост** | GitHub, GitLab, Gitea/Forgejo, Bitbucket — облачные и self-hosted, в том числе за файрволом. |
| **Трекеры задач** | Jira (Server/DC и Cloud), GitLab, GitHub, Gitea, Redmine, Bitbucket: задачи и обсуждения — источник для ответов, ответ возвращается в тот же тикет. |
| **Источники знаний** | Confluence, Notion, Google Диск, Figma, папки и сетевые диски (SMB), интернет. |
| **Свои серверы MCP** | Архив, репозиторий GitHub или удалённый сервер — с вашим явным согласием и пометкой «пользовательское». |
| **Каталог интеграций** | Подписанные комплекты: до установки видно, куда интеграция сможет ходить, какие учётные данные попросит и какие инструменты отдаст модели. |
| **Изоляция** | Каждое подключение — отдельный процесс под отдельным пользователем ОС; соседи не видят друг друга, секреты читает только ядро. |

<p align="center">
  <img src="assets/shots/connector-list.png" alt="Раздел «Коннекторы» в приложении: агенты, их статус и установленные интеграции" width="430">
  <img src="assets/shots/plugin-consent.png" alt="Каталог: границы доступа и инструменты интеграции до установки" width="430">
</p>

## Установка

В **Community** ничего ставить не нужно — коннектор встроен в [установку](https://github.com/buffsystems/buff-community). Для Enterprise и SaaS — бинарный файл или образ.

**Бинарный файл** — для Linux, macOS и Windows, в [выпусках](https://github.com/buffsystems/buff-connector/releases) этого репозитория. Скачайте его с суммами и подписью и проверьте **до** запуска — открытый ключ [`cosign.pub`](cosign.pub) лежит здесь же, в репозитории:

```sh
B=https://github.com/buffsystems/buff-connector/releases/latest/download
curl -fLO $B/connector-linux-amd64 -fLO $B/connector-linux-amd64.sig -fLO $B/SHA256SUMS
curl -fLO https://raw.githubusercontent.com/buffsystems/buff-connector/main/cosign.pub
sha256sum -c SHA256SUMS --ignore-missing
cosign verify-blob --key cosign.pub \
  --signature connector-linux-amd64.sig connector-linux-amd64
chmod +x connector-linux-amd64 && sudo mv connector-linux-amd64 /usr/local/bin/connector
connector pair --config /etc/connector/connector.yaml --code <код из приложения>
connector run  --config /etc/connector/connector.yaml
```

**Docker** (amd64/arm64, с `git` внутри):

```sh
docker run -d --name buff-connector \
  -v /srv/connector:/etc/connector \
  -v buff-connector-state:/var/lib/connector \
  ghcr.io/buffsystems/buff-connector:latest
```

Зеркала — `registry.buff.systems/buff/connector` для образа и [buff.systems/downloads](https://buff.systems/downloads) для файлов. Код сопряжения выдаёт приложение Buff в разделе «Коннекторы»; дальше настройка идёт на локальной странице агента. Подробно — [Установка](https://buff.systems/docs/connector/install) и [Локальная страница](https://buff.systems/docs/connector/web-setup).

## Свои интеграции в периметре

Внутренняя система, которой нет в каталоге, подключается без передачи её кода и данных нам:

- **Свой сервер MCP** — сервер вашего вендора, открытый или написанный вашей командой. Перетащите архив (`.mcpb`, Node.js, Python), назовите репозиторий GitHub или адрес удалённого сервера на локальной странице коннектора. Какие инструменты считать читающими — решаете вы. [Подробнее](https://buff.systems/docs/connector/custom-mcp).
- **Плагин на SDK** — [`sdk/buff_plugin`](sdk/buff_plugin) на стандартной библиотеке Python: плагин — это сервер MCP, который не держит учётных данных, а просит коннектор сделать запрос от его имени; коннектор подставляет ключ, сверяет адрес с разрешённым списком и записывает обращение в журнал.

Код интеграции остаётся на машине коннектора. Модели уходят описания инструментов и результаты их вызовов — ровно то, что нужно для ответа.

<p align="center">
  <img src="assets/shots/custom-mcp-add.png" alt="Раздел «Свои MCP» на локальной странице коннектора" width="600">
</p>

## Как это работает

```
        ваша сеть                                │        Buff
                                                 │
 ваш git ◀──локально──┐                          │
                      │                          │
 ваш трекер, wiki ◀───┤   КОННЕКТОР              │
                      ├── ядро: git-мост ────────┼──исходящее──▶ платформа Buff
 ваш сервер MCP ◀─────┘   интеграции: свой uid,  │               (CE · EE · SaaS)
                          свои разрешённые адреса│
```

- **Ядро маленькое и только git-мост** — его можно прочитать целиком; оно не растёт от каждой новой интеграции.
- **Интеграции — подписанные комплекты** из каталога; открытый ключ вшит в бинарь, подпись проверяется до распаковки.
- **Каталог лежит в реестре контейнеров** — закрытый периметр зеркалирует его в свой Harbor или Nexus и меняет только адрес.
- **Выпуски подписаны** cosign, подпись — в публичном журнале прозрачности.

Подробно — [Изоляция и границы доступа](https://buff.systems/docs/connector/isolation) и [Инструменты и модель](https://buff.systems/docs/connector/tools).

## Сборка из исходников

Нужен Go 1.22+.

```sh
make test     # модульные тесты
make vet
make build    # бинарь для своей системы
make dist     # все платформы в dist/ с SHA256SUMS
```

## Участие и лицензия

Ошибки и предложения — через [issues](https://github.com/buffsystems/buff-connector/issues), правки — pull request'ом; правила — в [CONTRIBUTING.md](CONTRIBUTING.md) (включая работу с ИИ-помощниками). Репозиторий публикуется снимком на каждый выпуск Buff: принятые изменения выходят в следующем.

Лицензия — [MIT](LICENSE). Вложенные библиотеки плагина файлов (pypdf, pyasn1, pysmb) — под своими лицензиями, рядом с кодом.
