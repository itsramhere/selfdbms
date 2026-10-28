import os
import sys
import hashlib
import csv

def sha256_bytes(b):
    return hashlib.sha256(b).hexdigest()

def normalize_lf(b):
    return b.replace(b'\r\n', b'\n')

def main():
    native_dir = '/home/ramst/selfdbms'
    checkout_dirs = {
        '9992ca2': '/home/ramst/checkouts/9992ca2',
        '370fd6c': '/home/ramst/checkouts/370fd6c',
        '81429fc': '/home/ramst/checkouts/81429fc',
        '4a06217': '/home/ramst/checkouts/4a06217',
    }

    # Find all source files relevant to building targeted_runner
    # This includes lsmengine, research/experiments/harness, research/experiments/targeted_validation, go.mod, go.sum, run_targeted_campaign.sh
    relevant_files = []
    for root, dirs, files in os.walk(native_dir):
        # Prune non-source directories
        rel_root = os.path.relpath(root, native_dir).replace('\\', '/')
        if rel_root.startswith('research/experiments/data'):
            continue
        if rel_root.startswith('research/checkpoints'):
            continue
        if rel_root.startswith('paper'):
            continue
        if rel_root.startswith('research/logs'):
            continue
        if rel_root.startswith('research/design'):
            continue
        if rel_root.startswith('research/debug'):
            continue

        for f in files:
            if f.endswith('.go') or f in ('go.mod', 'go.sum', 'run_targeted_campaign.sh', 'manifest.json'):
                rel_path = os.path.relpath(os.path.join(root, f), native_dir).replace('\\', '/')
                relevant_files.append(rel_path)

    relevant_files.sort()
    print(f"Total relevant source files: {len(relevant_files)}")

    matrix_rows = []

    # Target comparison revision: 370fd6c, 81429fc, 4a06217
    # Note that 370fd6c, 81429fc, 4a06217 have identical source code
    for rel in relevant_files:
        native_path = os.path.join(native_dir, rel)
        with open(native_path, 'rb') as fp:
            nat_raw = fp.read()
        nat_exact_sha = sha256_bytes(nat_raw)
        nat_norm_sha = sha256_bytes(normalize_lf(nat_raw))
        nat_has_crlf = b'\r\n' in nat_raw

        # Compare against 370fd6c
        git_path = os.path.join(checkout_dirs['370fd6c'], rel)
        if os.path.exists(git_path):
            with open(git_path, 'rb') as fp:
                git_raw = fp.read()
            git_exact_sha = sha256_bytes(git_raw)
            git_norm_sha = sha256_bytes(normalize_lf(git_raw))
            git_has_crlf = b'\r\n' in git_raw

            if nat_exact_sha == git_exact_sha:
                match_status = "BYTE_EXACT_MATCH"
                explanation = "Byte-for-byte identical"
            elif nat_norm_sha == git_norm_sha:
                match_status = "NORMALIZED_MATCH"
                explanation = f"Line-ending difference: native has {'CRLF' if nat_has_crlf else 'LF'}, git tree has {'CRLF' if git_has_crlf else 'LF'}"
            else:
                match_status = "MISMATCH"
                explanation = "Substantive content difference"
        else:
            git_exact_sha = "N/A"
            git_norm_sha = "N/A"
            match_status = "MISSING_IN_GIT"
            explanation = "File not present in candidate git revision"

        matrix_rows.append({
            'relative_path': rel,
            'native_sha256_exact': nat_exact_sha,
            'native_sha256_lf_norm': nat_norm_sha,
            'candidate_git_revision': '370fd6c',
            'git_sha256_exact': git_exact_sha,
            'git_sha256_lf_norm': git_norm_sha,
            'match_status': match_status,
            'explanation': explanation,
        })

    # Summary counts
    exact_count = sum(1 for r in matrix_rows if r['match_status'] == 'BYTE_EXACT_MATCH')
    norm_count = sum(1 for r in matrix_rows if r['match_status'] in ('BYTE_EXACT_MATCH', 'NORMALIZED_MATCH'))
    missing_count = sum(1 for r in matrix_rows if r['match_status'] == 'MISSING_IN_GIT')
    mismatch_count = sum(1 for r in matrix_rows if r['match_status'] == 'MISMATCH')

    print(f"Byte-exact matches: {exact_count}/{len(matrix_rows)}")
    print(f"Normalized matches (exact + CRLF-normalized): {norm_count}/{len(matrix_rows)}")
    print(f"Missing in git: {missing_count}")
    print(f"Substantive mismatches: {mismatch_count}")

    # Write SOURCE_MATCH_MATRIX.csv
    csv_path = '/mnt/c/Users/ramst/Documents/Projects/selfdbms/research/reviews/SOURCE_MATCH_MATRIX.csv'
    fieldnames = [
        'relative_path',
        'native_sha256_exact',
        'native_sha256_lf_norm',
        'candidate_git_revision',
        'git_sha256_exact',
        'git_sha256_lf_norm',
        'match_status',
        'explanation'
    ]
    with open(csv_path, 'w', newline='', encoding='utf-8') as f:
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        writer.writeheader()
        writer.writerows(matrix_rows)
    print(f"Wrote matrix to {csv_path}")

if __name__ == '__main__':
    main()
