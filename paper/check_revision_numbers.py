"""Read-only numerical provenance and optional static manuscript checks.

Default: list numeric tokens in the body text of paper.tex, paper_extended.tex
and paper/appendix.tex that are absent from both 63c6d21:paper.tex and
paper/generated/*, with original source line numbers. No rounding, arithmetic,
inferential analysis, or LaTeX execution is performed.
Use --verify for references, citations, macro definitions, math mode, source
hazards, and column counts after expanding generated table rows. Verification
treats paper.tex and paper_extended.tex (which alone inputs the appendix) as
separate documents: each is checked against only the generated files it inputs.
"""

from collections import Counter
from pathlib import Path
import argparse
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
MAIN, EXTENDED, APPENDIX = ROOT / 'paper.tex', ROOT / 'paper_extended.tex', ROOT / 'paper/appendix.tex'
FILES = (MAIN, EXTENDED, APPENDIX)
# Documents as compiled: the appendix is part of the extended report only.
UNITS = {'paper.tex': (MAIN,), 'paper_extended.tex': (EXTENDED, APPENDIX)}
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


def followup_macro_hazards(documents, definitions):
    """Read-only provenance check for every new scalar and generated row.

    The new generator also computes descriptive paired intervals from raw
    trials; verify those definitions without regenerating any output files.
    Numeric baseline handling stays unchanged: generated macros are already
    included in check_numbers()'s corpus.
    """
    import importlib.util
    directory = ROOT / 'research/experiments/analysis'
    sys.path.insert(0, str(directory))
    try:
        spec = importlib.util.spec_from_file_location('followup_paper_verify', directory / 'x_followup_paper_outputs.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        import json
        results = json.loads((module.X / 'analysis_followup/x_followup_results.json').read_text(encoding='utf-8'))
        rows = module.load_raw(module.X / 'followup', results)
        expected, _ = module.macro_values(results, rows)
        actual = {k: v for k, v in definitions.items() if k.startswith('Follow')}
        errors = ['Follow-up macro differs from raw/results provenance: ' + k
                  for k in sorted(set(actual) | set(expected)) if actual.get(k) != expected.get(k)]
        phase2 = r'\input{paper/generated/phase2}'
        followup = r'\input{paper/generated/followup}'
        for name, text in documents.items():
            if followup not in text:
                errors.append(name + ' does not load the follow-up macros')
            elif phase2 in text and text.index(followup) < text.index(phase2):
                errors.append(name + ': follow-up macros must be loaded after phase2')
        print(f'Follow-up macro provenance ({len(expected)} definitions): ' + ('PASS' if not errors else 'FAILED'))
        return errors
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print('Follow-up macro provenance: FAILED')
        return ['Follow-up provenance check: ' + str(exc)]
    finally:
        sys.path.pop(0)


def mainpaper_macro_hazards():
    """Recompute paper/generated/mainpaper.tex from retained results in memory.

    The restructured paper's new macros come from x_main_paper_outputs.py; the
    committed file must equal what the generator produces now, byte for byte.
    Nothing is written. The generator also asserts the claims the prose makes.
    """
    import importlib.util
    import json
    directory = ROOT / 'research/experiments/analysis'
    sys.path.insert(0, str(directory))
    try:
        spec = importlib.util.spec_from_file_location('main_paper_verify', directory / 'x_main_paper_outputs.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        x = module.X
        load = lambda p: json.loads(p.read_text(encoding='utf-8'))
        text, values = module.macro_text(
            load(x / 'analysis/x_readcost_results.json'), load(x / 'analysis/mechanism.json'),
            load(x / 'analysis_intervention/x_intervention_results.json'),
            load(x / 'analysis_followup/x_followup_results.json'), x / 'followup',
            load(x / 'analysis_settled/x_settled_results.json'), x)
        actual = (ROOT / 'paper/generated/mainpaper.tex').read_text(encoding='utf-8')
        errors = [] if text == actual else ['mainpaper.tex differs from x_main_paper_outputs.py output']
        print(f'Main-paper macro provenance ({len(values)} scalar definitions): ' + ('PASS' if not errors else 'FAILED'))
        return errors
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print('Main-paper macro provenance: FAILED')
        return ['Main-paper provenance check: ' + str(exc)]
    finally:
        sys.path.pop(0)


def main_document_hazards(main):
    """paper.tex stands alone: no appendix, no Linux numbers, no retracted claims."""
    errors = []
    clean = uncomment(main)
    for needle, message in (
            (r'\\appendices', 'paper.tex must not start appendices'),
            (r'\\input\{paper/appendix\}', 'paper.tex must not input the appendix'),
            (r'\\ifwithappendix', 'paper.tex must not use the appendix toggle'),
            (r'Appendix~?[A-C]\b', 'paper.tex refers to an appendix it does not have'),
            ):
        if re.search(needle, clean):
            errors.append(message)
    blocks = re.findall(r'(?<!\\newif)\\ifwithlinux\b(.*?)\\fi\b', main, flags=re.S)
    if len(blocks) != 1:
        errors.append('paper.tex must contain exactly one \\ifwithlinux block')
    for block in blocks:
        body = re.sub(r'\\(?:label|ref)\{[^}]*\}', '', uncomment(block))
        body = re.sub(r'\\begin\{tabular\}\{[^}]*\}', '', body)
        if re.search(r'\d', body):
            errors.append('the \\ifwithlinux block contains a digit; it may hold comments and headers only')
    if not re.search(r'\\newif\\ifwithlinux\\withlinuxfalse', main):
        errors.append('paper.tex must default to \\withlinuxfalse')
    print('Main-document checks (no appendix, no Linux numbers, retracted phrases): ' + ('PASS' if not errors else 'FAILED'))
    return errors


def phrase_hazards(name, text):
    """Phrases the restructuring retracted must not return in any document."""
    clean = uncomment(text)
    errors = []
    for needle, message in (
            (r'favoured', 'retracted phrase "favoured"'),
            (r'paid for in writes', 'retracted phrase "paid for in writes"'),
            (r'Lease earns its complexity', 'retracted phrase "Lease earns its complexity"'),
            (r'(?i:lock\s+handoff)', 'retracted phrase "lock handoff"'),
            (r'standard advice', 'uncited phrase "standard advice"'),
            (r'any reader-side design', 'over-general phrase "any reader-side design"'),
            (r'lookups up to 20', 'a 20 us dose is an injected delay, not a lookup time: say "injected delays up to 20"'),
            (r'bootstrap CIs~?\\cite\{efron1979\}', 'tighter-margin containment uses paired-t CIs, not bootstrap CIs'),
            (r'24--29', 'S = 0.705 is a 29.5% loss, so the range is 24--30'),
            (r'matched[^.]{0,80}until read-only', 'table-count claim "matched ... until read-only"')):
        if re.search(needle, clean):
            errors.append(f'{name}: {message}')
    return errors


def verify():
    """Static checks only; standard command exports are explicitly enumerated.

    This registry covers the used TeX primitives and the loaded IEEEtran,
    cite, amsmath/amssymb, graphicx, booktabs, url, and tikz interfaces.
    Custom commands must have actual source definitions, including in inputs.
    Each compiled document (paper.tex alone; paper_extended.tex with the
    appendix it inputs) is checked against the generated files it loads.
    """
    texts = {p: p.read_text(encoding='utf-8') for p in FILES}
    all_generated = list((ROOT / 'paper/generated').glob('*.tex'))
    definitions = generated_definitions(all_generated)
    required = math_required_macros(definitions)
    standard = set('''documentclass IEEEoverridecommandlockouts usepackage graphicspath
        input begin end raggedbottom title author IEEEauthorblockN IEEEauthorblockA
        textit maketitle section subsection label textbf texttt emph cite ref eqref
        path url item centering scriptsize small tiny setlength tabcolsep columnwidth
        textwidth caption toprule midrule bottomrule multicolumn includegraphics
        node filldraw fill draw foreach y xa xb mathrm text ln leq in mu pm times
        approx simeq widehat qquad noindent newcommand gdef newif clearpage appendices fi alpha
        bibitem mathcal textsf neg cdot lesssim geq ensuremath'''.split())
    errors = []
    if r'\input{paper/appendix}' in texts[MAIN] or r'\input{paper/appendix}' not in texts[EXTENDED]:
        errors.append('only paper_extended.tex may input the appendix')
    errors.extend(followup_macro_hazards({m.name: texts[m] for m in (MAIN, EXTENDED)}, definitions))
    errors.extend(mainpaper_macro_hazards())
    errors.extend(main_document_hazards(texts[MAIN]))
    for path in FILES:
        errors.extend(phrase_hazards(path.name, texts[path]))
    math_failures, syntax_failures, table_failures = [], [], []
    table_total = row_total = 0
    used_total = label_total = ref_total = bib_total = 0
    for unit, paths in UNITS.items():
        sources = '\n'.join(texts[p] for p in paths)
        loaded = []
        for name in re.findall(r'\\input\{(paper/generated/[^}]+)\}', uncomment(sources)):
            path = ROOT / (name + '.tex')
            if not path.is_file():
                errors.append(f'{unit}: missing generated input {name}')
            else:
                loaded.append(path)
        expanded = sources + '\n' + '\n'.join(p.read_text(encoding='utf-8') for p in loaded)
        clean = uncomment(expanded)
        defined = set(re.findall(r'\\(?:newcommand|renewcommand|providecommand)\{\\([A-Za-z]+)\}', clean))
        defined.update(re.findall(r'\\(?:gdef|def)\s*\\([A-Za-z]+)', clean))
        for name in re.findall(r'\\newif\\if([A-Za-z]+)', clean):
            defined.update(('if' + name, name + 'true', name + 'false'))
        # TikZ loop variables are local macro definitions.
        for variables in re.findall(r'\\foreach\s+([^\s]+)\s+in', clean):
            defined.update(re.findall(r'\\([A-Za-z]+)', variables))
        used = set(re.findall(r'\\([A-Za-z]+)', clean))
        unknown = sorted(used - defined - standard)
        labels = re.findall(r'\\label\{([^}]+)\}', sources)
        refs = re.findall(r'\\(?:ref|eqref)\{([^}]+)\}', sources)
        cited = {k.strip() for group in re.findall(r'\\cite\{([^}]+)\}', sources) for k in group.split(',')}
        bib = re.findall(r'\\bibitem\{([^}]+)\}', sources)
        for label, values in (
            ('Undefined macros', unknown),
            ('Undefined references', sorted(set(refs) - set(labels))),
            ('Undefined citations', sorted(cited - set(bib))),
            ('Uncited bibliography entries', sorted(set(bib) - cited)),
            ('Duplicate labels', sorted(k for k, n in Counter(labels).items() if n > 1)),
        ):
            print(f'{unit}: {label}: {", ".join(values) if values else "none"}')
            errors.extend(f'{unit}: {label}: {v}' for v in values)
        used_total += len(used)
        label_total += len(labels)
        ref_total += len(refs)
        bib_total += len(bib)
        for path in paths:
            source = texts[path]
            def locate(issues, path=path, source=source):
                return [f'{path.relative_to(ROOT).as_posix()}:{source.count(chr(10), 0, pos) + 1}: {message}'
                        for pos, message in issues]
            math_errors, syntax_errors = tex_hazards(source, required)
            table_errors, tables, rows = table_hazards(source, definitions)
            math_failures.extend(locate(math_errors))
            syntax_failures.extend(locate(syntax_errors + moving_argument_hazards(source)))
            table_failures.extend(locate(table_errors))
            table_total += tables
            row_total += rows
        # Switched-off blocks must not leave references dangling: with the
        # toggle false, only the remaining labels exist.
        main = texts[paths[0]]
        disabled = re.sub(r'(?<!\\newif)\\ifwithappendix\b.*?\\fi\b', '', main, flags=re.S)
        disabled = re.sub(r'(?<!\\newif)\\ifwithlinux\b.*?\\fi\b', '', disabled, flags=re.S)
        disabled_labels = set(re.findall(r'\\label\{([^}]+)\}', disabled))
        disabled_refs = set(re.findall(r'\\(?:ref|eqref)\{([^}]+)\}', disabled))
        if len(paths) == 1:
            for ref in disabled_refs - disabled_labels:
                errors.append(f'{unit}: reference to a switched-off block: {ref}')
        else:
            for ref in disabled_refs - (disabled_labels | set(re.findall(r'\\label\{([^}]+)\}', texts[paths[1]]))):
                errors.append(f'{unit}: reference to a switched-off block: {ref}')
    for name, failures in (
        (f'Math-mode check ({len(required)} generated math-only macros)', math_failures),
        ('TeX source hazards', syntax_failures),
        (f'Table column counts ({table_total} tables, {row_total} expanded rows)', table_failures),
    ):
        print(f'{name}: {"; ".join(failures) if failures else "PASS"}')
        errors.extend(failures)
    if errors:
        print('Static verification: FAILED')
        for error in errors:
            print('  ' + error)
        return False
    print(f'Static verification: PASS ({used_total} command names, {label_total} labels, {ref_total} references, {bib_total} cited bibliography entries, across both documents).')
    print('Standard commands checked against explicit loaded-package registry; no LaTeX run.')
    return True


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify', action='store_true')
    args = parser.parse_args()
    check_numbers()
    if args.verify and not verify():
        sys.exit(1)
