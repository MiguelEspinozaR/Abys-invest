-- 018_fix_finviz_html_entities.sql
--
-- Des-escape de las entidades HTML que el fallback a Finviz dejó PERSISTIDAS en
-- securities.sector/industry. Medido en dev: 11 de 44 filas del catálogo con
-- literales de entidad (`Oil &amp; Gas E&P`, `Aerospace &amp; Defense`,
-- `Farm &amp; Heavy Construction Machinery`, `Household &amp; Personal
-- Products`…). `sector` salió limpio (0 filas) en esa muestra, pero se cubre
-- por simetría: el arreglo es del contrato del parser, no del dato de hoy.
--
-- Causa: el adaptador de Finviz (internal/collect/finviz/client.go) saca el
-- texto de las regex sectorLinkRe/industryLinkRe sobre el HTML CRUDO y lo
-- asignaba sin des-escapar. industryLinkRe captura un atributo title="...", que
-- está escapado por definición; sectorLinkRe captura el texto del link, también
-- escapado. El pipeline es Yahoo primero y Finviz de fallback
-- (internal/collect/yahoo/update_sector.go): el probe que dejó estas 11 filas
-- obtuvo HTTP 429 de Yahoo (rate limit por IP desde este host), que degradó 11
-- de 44 tickers al fallback. El 429 es contexto externo — no se toca el cliente
-- de Yahoo ni su manejo de errores.
--
-- El fix del parser (W1 de esta misma tarea: html.UnescapeString en
-- parseQuotePage) evita que el síntoma vuelva; esta migración repara el dato YA
-- escrito, porque arreglar el parser no reescribe historia.
--
-- Por qué no importa para `relative`: los comparables se agrupan por SECTOR
-- (internal/storage/comparables.go → WHERE s.sector = $1), no por industry. El
-- bug ensucia una etiqueta que devuelve la API (GET /securities,
-- GET /securities/{ticker}) y que la UI muestra; no mueve medianas ni conteos.
--
-- Decisiones de implementación:
--
-- * replace() inline y NO una función SQL permanente (CREATE OR REPLACE
--   finviz_unescape(text)): es autocontenida, no añade objetos al esquema, no
--   deja nada detrás y se lee completa en un solo sitio. Una función permanente
--   solo se justificaría si el decode creciera (más entidades, más usos); no es
--   el caso.
-- * Cadena explícita y determinista sobre el conjunto CERRADO de 7 entidades,
--   todas ellas CON punto y coma: &amp;, &lt;, &gt;, &quot;, &#39;, &apos;,
--   &nbsp;. Si Finviz empieza a emitir otra (&eacute;, &hellip;), el sitio donde
--   añadirla es el test de W1 en internal/collect/finviz/client_test.go.
-- * ORDEN: las entidades de puntos y comillas primero, &amp; AL FINAL. Es la
--   razón por la que replace() (que es una pasada, sin reescaneo) y
--   html.UnescapeString coinciden: en `&amp;#39;` el `&#39;` no aparece como
--   subcadena hasta que `&amp;` ya se convirtió, así que la decodificación es de
--   UN nivel, igual que en Go. Un valor doble-escapado queda en `&#39;` en
--   ambos lados; es lo correcto (dos niveles exigiría un segundo parseo y el
--   parser tampoco lo hace). Con el orden inverso, en cambio, `&amp;lt;` se
--   des-escaparía dos veces en la misma pasada y el SQL divergiría del parser.
-- * ALCANCE DE LA EQUIVALENCIA CON EL PARSER: es deliberadamente PARCIAL, no
--   total. La equivalencia con html.UnescapeString se afirma únicamente sobre
--   el conjunto cerrado de 7 entidades CON punto y coma de la cadena de arriba.
--   Go decodifica además tres formas que esta migración NO cubre, y se dejan
--   fuera A PROPÓSITO (no es un olvido):
--     - hexadecimal: `&#x26;` (= "&"), no está en la cadena ni la casa el
--       predicado del WHERE (que exige `;` y la forma `&#39;`);
--     - sin punto y coma: `&amp` (= "&"), indistinguible para el `WHERE`;
--     - en mayúsculas: `&AMP;` (= "&"), porque `replace()` y el `~` de Postgres
--       son case-sensitive.
--   Motivo de dejarlas fuera: no aparecen en el dato medido (11 filas de 44, todas
--   con la forma canónica terminada en `;`) y ampliar el WHERE/SET exigiría
--   revisar el predicado para que siga siendo idempotente. Está registrado como
--   deuda en §8.2 del plan abys-finviz-unescape ("entidades no contempladas"), y
--   el parser (W1) sí las cubre: una fila NUEVA que llegue con esas formas se
--   persiste ya des-escapada, así que el desfase sólo podría volver sobre datos
--   previos a este fix. Los 3 casos opuestos del test de W1 fijan el lado Go.
-- * &nbsp; → espacio normal, no U+00A0. Divergencia deliberada y sin efecto
--   sobre el parser: un U+00A0 en el medio de la etiqueta sobrevive al
--   TrimSpace del parser, así que la fila que lo contiene NUNCA entra por el
--   WHERE de esta migración (no tiene entidad). Para una etiqueta que se
--   compara y se copia, el espacio normal es el valor correcto.
-- * El WHERE replica el predicado del SET —el mismo conjunto cerrado de
--   entidades— de modo que NUNCA es un UPDATE masivo: una fila sin entidad no
--   se toca, y una fila ya decodificada tampoco (tras aplicar la cadena, ninguna
--   entidad sobrevive, así que la segunda pasada afecta 0 filas: es idempotente,
--   y eso es lo que exige RunMigrations al correr el archivo dos veces).
--   Equivalencia comprobada: toda fila que cumple el predicado cambia de valor
--   (cada entidad se convierte en un carácter de longitud distinta) y toda fila
--   que no lo cumple sale igual.
-- * Reversible: la operación es la inversa exacta (replace('&', '&amp;') la
--   deshace), aunque eso reintroduciría el bug: el dato bueno es el des-escapado.
--
-- Idempotente y autocontenida: sin DDL, sin funciones, sin tocar el esquema.

UPDATE securities
SET industry = replace(replace(replace(replace(replace(replace(replace(industry,
            '&lt;', '<'),
            '&gt;', '>'),
            '&quot;', '"'),
            '&#39;', ''''),
            '&apos;', ''''),
            '&nbsp;', ' '),
            '&amp;', '&'),
    sector = replace(replace(replace(replace(replace(replace(replace(sector,
            '&lt;', '<'),
            '&gt;', '>'),
            '&quot;', '"'),
            '&#39;', ''''),
            '&apos;', ''''),
            '&nbsp;', ' '),
            '&amp;', '&')
WHERE industry ~ '&(amp|lt|gt|quot|apos|nbsp|#39);'
   OR sector   ~ '&(amp|lt|gt|quot|apos|nbsp|#39);';
