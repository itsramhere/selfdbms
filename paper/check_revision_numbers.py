"""Read-only numerical provenance and optional static manuscript checks.

Default: list numeric tokens in manuscript body text absent from both
63c6d21:paper.tex and paper/generated/*, with original source line numbers.
No rounding, arithmetic, inferential analysis, or LaTeX execution is performed.
Use --verify for references, citations, macro definitions, math mode, source
hazards, and column counts after expanding generated table rows.
"""

from collections import Counter
from pathlib import Path
import argparse
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
FILES = (ROOT / 'paper.tex', ROOT / 'paper/appendix.tex')
NUMBER = re.compile(r'(?<![\w.])\d+(?:(?:[.,]|\{,\})\d+)*(?![\w.])')


def mask(match):
    return ''.join('\n' if c == '\n' else ' ' for c in match[0])


def body_text(source):
    """Mask non-prose TeX while retaining offsets and source line numbers.

    Captions, tables' actual cells, inline math, and ordinary prose remain.
    TikZ coordinates, lengths, input paths, labels, bibliography, and revision
    hashes are source machinery rather than numerical claims in body text.
    """
    start = source.find(r'\begin{abstract}')
    if start >= 0:
        source = mask(re.match(r'[\s\S]*', source[:start])) + source[start:]
    patterns = (
        r'(?m)(?<!\\)%[^\n]*',
        r'\\begin\{thebibliography\}.*?\\end\{thebibliography\}',
        r'\\begin\{tikzpicture\}.*?\\end\{tikzpicture\}',
        r'\\(?:label|ref|eqref|cite|url|path|input|graphicspath)\{[^}]*\}',
        r'\\includegraphics(?:\[[^]]*\])?\{[^}]*\}',
        r'\\setlength\{[^}]*\}\{[^}]*\}',
        r'\\begin\{tabular\}\{[^\n]*',
        r'\\texttt\{[0-9a-f]{7,}\}',
    )
    for pattern in patterns:
        source = re.sub(pattern, mask, source, flags=re.S)
    return source


def number_tokens(source):
    # TeX thousands separators must match ordinary thousands separators.
    return {m[0].replace('{,}', ',').replace(',', '') for m in NUMBER.finditer(source)}


def check_numbers():
    old = subprocess.run(
        ['git', 'show', '63c6d21:paper.tex'], cwd=ROOT, check=True,
        capture_output=True, encoding='utf-8',
    ).stdout
    corpus = old + '\n' + '\n'.join(
        p.read_text(encoding='utf-8')
        for p in sorted((ROOT / 'paper/generated').glob('*')) if p.is_file()
    )
    known = number_tokens(corpus)
    additions = []
    for path in FILES:
        source = body_text(path.read_text(encoding='utf-8'))
        for match in NUMBER.finditer(source):
            if match[0].replace('{,}', ',').replace(',', '') not in known:
                line = source.count('\n', 0, match.start()) + 1
                additions.append((path.relative_to(ROOT).as_posix(), line, match[0]))
    print('Numerical tokens absent from 63c6d21:paper.tex and paper/generated/*:')
    for path, line, token in additions:
        print(f'{path}:{line}: {token}')
    if not additions:
        print('None.')
    return additions


def tokens(source):
    """Tokenize control words/symbols, comments, and ordinary characters.

    In particular, \\% is a literal percent, whereas \\\\ followed by % starts
    a comment. Keeping offsets allows every diagnostic to name a source line.
    """
    i = 0
    while i < len(source):
        start = i
        if source[i] == '\\':
            match = re.match(r'\\([A-Za-z@]+|[^\n])', source[i:])
            if match:
                i += len(match[0])
                if match[1] in {'path', 'url'}:
                    # url.sty reads these arguments with literal %/#/_ tokens.
                    # Consume them here so a literal % cannot hide later text.
                    while i < len(source) and source[i].isspace():
                        i += 1
                    if i >= len(source):
                        raise ValueError('missing literal path/url argument')
                    if source[i] == '{':
                        depth = 1
                        i += 1
                        while i < len(source) and depth:
                            if source[i] == '\\':
                                i += 2
                                continue
                            if source[i] == '{':
                                depth += 1
                            elif source[i] == '}':
                                depth -= 1
                            i += 1
                        if depth:
                            raise ValueError('unclosed literal path/url argument')
                    else:
                        delimiter = source[i]
                        close = source.find(delimiter, i + 1)
                        if close < 0:
                            raise ValueError('unclosed delimited path/url argument')
                        i = close + 1
                    yield 'literal', match[1], start, i
                    continue
                yield 'command', match[1], start, i
                continue
        if source[i] == '%':
            end = source.find('\n', i)
            i = len(source) if end < 0 else end
            yield 'comment', source[start:i], start, i
            continue
        i += 1
        yield 'char', source[start], start, i


def uncomment(source):
    chars = list(source)
    for kind, _, start, end in tokens(source):
        if kind == 'comment':
            chars[start:end] = ' ' * (end - start)
    return ''.join(chars)


def group(source, start, opening='{', closing='}'):
    """Read a balanced argument, respecting comments and escaped braces."""
    while start < len(source) and source[start].isspace():
        start += 1
    if start >= len(source) or source[start] != opening:
        raise ValueError(f'expected {opening} argument')
    depth = 0
    for kind, value, offset, end in tokens(source[start:]):
        if kind != 'char':
            continue
        if value == opening:
            depth += 1
        elif value == closing:
            depth -= 1
            if depth == 0:
                return source[start + 1:start + offset], start + end
    raise ValueError(f'unclosed {opening} argument')


def generated_definitions(paths):
    definitions = {}
    for path in paths:
        source = uncomment(path.read_text(encoding='utf-8'))
        pattern = r'\\(?:newcommand|renewcommand|providecommand)\{\\([A-Za-z]+)\}|\\(?:gdef|def)\s*\\([A-Za-z]+)'
        for match in re.finditer(pattern, source):
            name = match[1] or match[2]
            body, _ = group(source, match.end())
            definitions[name] = body
    return definitions


MATH_COMMANDS = set('''times cdot pm mp mu sigma alpha beta gamma delta theta
    lambda pi rho tau phi chi omega in notin leq geq neq approx simeq lesssim
    gtrsim ln log exp frac dfrac tfrac sqrt sum prod int infty
    mathrm mathcal mathsf mathbf mathit widehat hat bar overline
    left right'''.split())
MATH_ENVS = {'math', 'displaymath', 'equation', 'equation*', 'align', 'align*',
             'gather', 'gather*', 'multline', 'multline*', 'eqnarray', 'eqnarray*'}
ALIGN_ENVS = {'tabular', 'tabular*', 'array', 'align', 'align*', 'aligned',
              'eqnarray', 'eqnarray*', 'matrix', 'pmatrix', 'bmatrix', 'cases'}
FLOAT_ENVS = {'table', 'table*', 'figure', 'figure*'}
METADATA = {'label', 'ref', 'eqref', 'cite', 'bibitem', 'input', 'includegraphics',
            'graphicspath', 'documentclass', 'usepackage', 'url', 'path'}


def tex_hazards(source, required=(), syntax=True):
    """Inspect this manuscript's TeX subset without executing or compiling it.

    Generated definitions are inspected with syntax=False: row macros contain
    legitimate alignment tokens, and self-wrapped math needs no outer dollars.
    """
    math_errors, errors = [], []
    mode = None
    environments, braces = [], []
    skip_until = 0
    for kind, value, start, end in tokens(source):
        if start < skip_until:
            continue
        if kind == 'literal':
            continue
        if kind == 'comment':
            prefix = source[source.rfind('\n', 0, start) + 1:start]
            if syntax and prefix.strip():
                errors.append((start, 'unescaped % in prose or unintended inline comment'))
            continue
        if kind == 'command':
            if value in {'begin', 'end'}:
                name, skip_until = group(source, end)
                if value == 'begin':
                    if syntax and name in FLOAT_ENVS and any(n in FLOAT_ENVS for n in environments):
                        errors.append((start, 'float inside a float'))
                    environments.append(name)
                    if name in MATH_ENVS:
                        if mode:
                            errors.append((start, 'nested math environment'))
                        mode = name
                else:
                    if not environments or environments.pop() != name:
                        errors.append((start, 'mismatched environment ' + name))
                    if name in MATH_ENVS:
                        if mode != name:
                            errors.append((start, 'mismatched math environment ' + name))
                        mode = None
                continue
            if value in METADATA or value == 'ensuremath':
                pos = end
                while pos < len(source) and source[pos].isspace():
                    pos += 1
                if pos < len(source) and source[pos] == '[':
                    _, pos = group(source, pos, '[', ']')
                _, skip_until = group(source, pos)
                continue
            if value in {'(', '['}:
                if mode:
                    errors.append((start, 'nested math delimiter \\' + value))
                mode = value
            elif value in {')', ']'}:
                if mode != {')': '(', ']': '['}[value]:
                    errors.append((start, 'unmatched math delimiter \\' + value))
                mode = None
            elif not mode and (value in required or value in MATH_COMMANDS):
                math_errors.append((start, '\\' + value + ' requires math mode'))
            continue
        if value == '$':
            delimiter = '$$' if source.startswith('$$', start) else '$'
            if delimiter == '$$':
                skip_until = end + 1
            if mode is None:
                mode = delimiter
            elif mode == delimiter:
                mode = None
            else:
                errors.append((start, 'mismatched $ math delimiter'))
        elif value in '^_' and not mode:
            math_errors.append((start, 'unescaped ' + value + ' outside math mode'))
        elif syntax and (value == '#' or value == '&' and not any(n in ALIGN_ENVS for n in environments)):
            errors.append((start, 'unescaped ' + value + ' in prose'))
        elif syntax and value == '{':
            braces.append(start)
        elif syntax and value == '}':
            if braces:
                braces.pop()
            else:
                errors.append((start, 'unmatched closing brace'))
    if mode:
        errors.append((len(source), 'unclosed math delimiter/environment ' + mode))
    if syntax and braces:
        errors.append((braces[-1], 'unclosed brace'))
    if environments:
        errors.append((len(source), 'unclosed environments ' + ', '.join(environments)))
    return math_errors, errors


def math_required_macros(definitions):
    required = set()
    # Propagate requirements through aliases, not just literal \\times/^/_ uses.
    while True:
        found = {name for name, body in definitions.items()
                 if tex_hazards(body, required, syntax=False)[0]}
        if found <= required:
            return required
        required.update(found)


def column_count(spec):
    count, pos = 0, 0
    while pos < len(spec):
        char = spec[pos]
        pos += 1
        if char in 'lcrXS':
            count += 1
        elif char in 'pmb':
            _, pos = group(spec, pos)
            count += 1
        elif char in '@!><':
            _, pos = group(spec, pos)
        elif char == '*':
            repetitions, pos = group(spec, pos)
            repeated, pos = group(spec, pos)
            count += int(repetitions) * column_count(repeated)
        elif not char.isspace() and char not in '|:':
            raise ValueError('unsupported column specifier ' + char)
    return count


def table_hazards(source, definitions):
    errors, tables, rows = [], 0, 0
    source = uncomment(source)
    for match in re.finditer(r'\\begin\{tabular\}', source):
        spec, end = group(source, match.end())
        expected = column_count(spec)
        finish = source.index(r'\end{tabular}', end)
        body = source[end:finish]
        # Expand row macros and scalar generated macros without changing files.
        for _ in range(len(definitions) + 1):
            expanded = re.sub(r'\\([A-Za-z]+)(?:\{\})?',
                              lambda m: definitions.get(m[1], m[0]), body)
            if expanded == body:
                break
            body = expanded
        else:
            raise ValueError('recursive generated table macro')
        body = re.sub(r'\\(?:toprule|midrule|bottomrule)\b', '', body)
        tables += 1
        depth, cells, cell_start = 0, [], 0
        for kind, value, start, stop in tokens(body):
            if kind == 'char' and value == '{':
                depth += 1
            elif kind == 'char' and value == '}':
                depth -= 1
            elif depth == 0 and ((kind == 'char' and value == '&') or (kind == 'command' and value == '\\')):
                cells.append(body[cell_start:start])
                cell_start = stop
                if kind == 'command':
                    actual = 0
                    for cell in cells:
                        multicolumn = re.match(r'\s*\\multicolumn\{(\d+)\}', cell)
                        actual += int(multicolumn[1]) if multicolumn else 1
                    rows += 1
                    if actual != expected:
                        errors.append((match.start(), f'table row {rows}: {actual} columns, expected {expected}'))
                    cells = []
        if body[cell_start:].strip() or cells:
            errors.append((match.start(), 'table has an unterminated final row'))
    return errors, tables, rows


def moving_argument_hazards(source):
    errors = []
    source = uncomment(source)
    for match in re.finditer(r'\\(?:caption|section|subsection|title|author)\*?', source):
        pos = match.end()
        while pos < len(source) and source[pos].isspace():
            pos += 1
        arguments = []
        if pos < len(source) and source[pos] == '[':
            argument, pos = group(source, pos, '[', ']')
            arguments.append(argument)
        argument, _ = group(source, pos)
        arguments.append(argument)
        if any(re.search(r'\\path\b', argument) for argument in arguments):
            errors.append((match.start(), '\\path in a moving argument'))
    return errors


def verify():
    """Static checks only; standard command exports are explicitly enumerated.

    This registry covers the used TeX primitives and the loaded IEEEtran,
    cite, amsmath/amssymb, graphicx, booktabs, url, and tikz interfaces.
    Custom commands must have actual source definitions, including in inputs.
    """
    documents = {p: p.read_text(encoding='utf-8') for p in FILES}
    inputs = list((ROOT / 'paper/generated').glob('*.tex'))
    sources = '\n'.join(documents.values())
    expanded = sources + '\n' + '\n'.join(p.read_text(encoding='utf-8') for p in inputs)
    clean = uncomment(expanded)
    defined = set(re.findall(r'\\(?:newcommand|renewcommand|providecommand)\{\\([A-Za-z]+)\}', clean))
    defined.update(re.findall(r'\\(?:gdef|def)\s*\\([A-Za-z]+)', clean))
    for name in re.findall(r'\\newif\\if([A-Za-z]+)', clean):
        defined.update(('if' + name, name + 'true', name + 'false'))
    # TikZ loop variables are local macro definitions.
    for variables in re.findall(r'\\foreach\s+([^\s]+)\s+in', clean):
        defined.update(re.findall(r'\\([A-Za-z]+)', variables))
    standard = set('''documentclass IEEEoverridecommandlockouts usepackage graphicspath
        input begin end raggedbottom title author IEEEauthorblockN IEEEauthorblockA
        textit maketitle section subsection label textbf texttt emph cite ref eqref
        path url item centering scriptsize small tiny setlength tabcolsep columnwidth
        textwidth caption toprule midrule bottomrule multicolumn includegraphics
        node filldraw fill draw foreach y xa xb mathrm text ln leq in mu pm times
        approx simeq widehat qquad noindent newcommand gdef newif clearpage appendices fi
        bibitem mathcal textsf neg cdot lesssim geq ensuremath'''.split())
    used = set(re.findall(r'\\([A-Za-z]+)', clean))
    unknown = sorted(used - defined - standard)
    labels = re.findall(r'\\label\{([^}]+)\}', sources)
    refs = re.findall(r'\\(?:ref|eqref)\{([^}]+)\}', sources)
    cited = {k.strip() for group in re.findall(r'\\cite\{([^}]+)\}', sources) for k in group.split(',')}
    bib = re.findall(r'\\bibitem\{([^}]+)\}', sources)
    errors = []
    for name, values in (
        ('Undefined macros', unknown),
        ('Undefined references', sorted(set(refs) - set(labels))),
        ('Undefined citations', sorted(cited - set(bib))),
        ('Uncited bibliography entries', sorted(set(bib) - cited)),
        ('Duplicate labels', sorted(k for k, n in Counter(labels).items() if n > 1)),
    ):
        print(f'{name}: {", ".join(values) if values else "none"}')
        errors.extend(values)
    definitions = generated_definitions(inputs)
    required = math_required_macros(definitions)
    math_failures, syntax_failures, table_failures = [], [], []
    table_total = row_total = 0
    for path, source in documents.items():
        def locate(issues):
            return [f'{path.relative_to(ROOT).as_posix()}:{source.count(chr(10), 0, pos) + 1}: {message}'
                    for pos, message in issues]
        math_errors, syntax_errors = tex_hazards(source, required)
        table_errors, tables, rows = table_hazards(source, definitions)
        math_failures.extend(locate(math_errors))
        syntax_failures.extend(locate(syntax_errors + moving_argument_hazards(source)))
        table_failures.extend(locate(table_errors))
        table_total += tables
        row_total += rows
    for name, failures in (
        (f'Math-mode check ({len(required)} generated math-only macros)', math_failures),
        ('TeX source hazards', syntax_failures),
        (f'Table column counts ({table_total} tables, {row_total} expanded rows)', table_failures),
    ):
        print(f'{name}: {"; ".join(failures) if failures else "PASS"}')
        errors.extend(failures)
    # Disabling appendices must not leave main-body object references dangling.
    main = documents[FILES[0]]
    main_labels = set(re.findall(r'\\label\{([^}]+)\}', main))
    main_refs = set(re.findall(r'\\(?:ref|eqref)\{([^}]+)\}', main))
    for ref in main_refs - main_labels:
        errors.append('Appendix-dependent main-body reference: ' + ref)
    if errors:
        print('Static verification: FAILED')
        for error in errors:
            print('  ' + error)
        return False
    print(f'Static verification: PASS ({len(used)} command names, {len(labels)} labels, {len(refs)} references, {len(bib)} cited bibliography entries).')
    print('Standard commands checked against explicit loaded-package registry; no LaTeX run.')
    return True


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify', action='store_true')
    args = parser.parse_args()
    check_numbers()
    if args.verify and not verify():
        sys.exit(1)
