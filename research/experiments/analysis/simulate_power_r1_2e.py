#!/usr/bin/env python3
"""
simulate_power_r1_2e.py
Phase I7 Checkpoint R1.2e: Corrected Statistical Power and Estimand Simulation Suite
Evaluated Git Revision: f4cce4b5faa80924c7ef945139ef1a65ccb2b32a

Resolves all external review blockers with machine-verified mathematics:
1. Workload-specific sensitivity calculations (sigmas in [0.06, 0.12, 0.18, 0.25]).
2. Prospective pilot calibration rules (N_pilot=4) and bounded confirmatory sizing N in [8, 10].
   Empirical write effect factor: (z_0.975 + z_0.80)^2 / (mu_d - delta_NI)^2 = 884.14.
   Degrees of freedom adjustment: df = N - 2 to strictly preserve Type I error <= 0.0250.
3. Bretz graphical multiple testing procedure (alpha_1=0.025 Efficacy, alpha_2=0.025 Causal).
4. Restored EXP-2 CPU-load-matched negative control (C2-06: T_in=0, T_out=300 us, r=4).
5. Zero-safe write rate difference Delta W_dot and non-parametric composite ranking for starvation.
6. Paired directional sign test and Restricted Mean Survival Time (RMST) non-inferiority under censoring.

ZERO BENCHMARK EXECUTION: Purely analytical and synthetic statistical modeling.
"""

import math
import json
import os
import sys
import numpy as np
from scipy import stats

def compute_t_power(n, mu_d, margin, sigma, alpha=0.025, df_adj=2):
    """Computes analytical power for a one-sided paired Student-t non-inferiority test with df adjustment."""
    df = n - df_adj
    if df < 1:
        return 0.0
    se = sigma / math.sqrt(n)
    ncp = (mu_d - margin) / se
    t_crit = stats.t.ppf(1.0 - alpha, df)
    power = 1.0 - stats.nct.cdf(t_crit, df, ncp)
    return float(np.clip(power, 0.0, 1.0))

def compute_required_n(sigma, margin, mu_d=-0.01114, alpha=0.025, power=0.80):
    """
    Computes sample size required for target power under empirical effect mu_d.
    Formula: N = ceil( (z_{1-alpha} + z_{power})^2 * sigma^2 / (mu_d - margin)^2 )
    For alpha=0.025, power=0.80, margin=ln(0.90) = -0.10536, mu_d=-0.01114, factor is 884.14.
    """
    z_alpha = stats.norm.ppf(1.0 - alpha)
    z_beta = stats.norm.ppf(power)
    delta = abs(mu_d - margin)
    factor = ((z_alpha + z_beta) / delta) ** 2
    n_approx = factor * (sigma ** 2)
    return int(math.ceil(n_approx)), float(factor)

def cf_power_delta(n, cf_true, mu_b=265.0, mu_a=0.5, cv=0.08, rho=0.85, alpha=0.025):
    """
    Computes asymptotic power to establish lower 95% BCa CI >= 0.70 via Taylor series delta method
    for the exact ratio of differences: CF = (W_amin - W_a) / (W_b - W_a).
    """
    denom = mu_b - mu_a
    sigma_b = cv * mu_b
    sigma_amin = cv * mu_b
    sigma_a = cv * max(mu_a, 1.0)
    
    g_amin = 1.0 / denom
    g_b = -cf_true / denom
    g_a = (cf_true - 1.0) / denom
    
    cov_amin_b = rho * sigma_amin * sigma_b / n
    cov_amin_a = (rho / 2.0) * sigma_amin * sigma_a / n
    cov_b_a = (rho / 2.0) * sigma_b * sigma_a / n
    
    var_amin = (sigma_amin ** 2) / n
    var_b = (sigma_b ** 2) / n
    var_a = (sigma_a ** 2) / n
    
    var_cf = (g_amin**2)*var_amin + (g_b**2)*var_b + (g_a**2)*var_a \
             + 2*g_amin*g_b*cov_amin_b + 2*g_amin*g_a*cov_amin_a + 2*g_b*g_a*cov_b_a
    se_cf = math.sqrt(max(var_cf, 1e-9))
    
    z = (cf_true - 0.70) / se_cf
    z_crit = stats.norm.ppf(1.0 - alpha)
    return float(np.clip(1.0 - stats.norm.cdf(z_crit - z), 0.0, 1.0)), float(se_cf)

def simulate_exp4_sign_test(n_pairs, mu_log_a, sigma_log_a, mu_log_amin, sigma_log_amin, t_max=30.0, alpha=0.025, n_sims=5000):
    """Simulates exact one-sided binomial test power for paired directional sign test under right-censoring."""
    np.random.seed(2026 + n_pairs)
    t_a = np.random.lognormal(mean=mu_log_a, sigma=sigma_log_a, size=(n_sims, n_pairs))
    t_amin = np.random.lognormal(mean=mu_log_amin, sigma=sigma_log_amin, size=(n_sims, n_pairs))
    
    c_a = t_a >= t_max
    c_amin = t_amin >= t_max
    t_a_obs = np.minimum(t_a, t_max)
    t_amin_obs = np.minimum(t_amin, t_max)
    
    plus_one = (~c_amin) & (t_amin_obs < t_a_obs)
    minus_one = (~c_a) & (t_a_obs < t_amin_obs)
    
    rejections = 0
    for i in range(n_sims):
        n_plus = np.sum(plus_one[i])
        n_minus = np.sum(minus_one[i])
        n_eff = n_plus + n_minus
        if n_eff == 0:
            continue
        pval = stats.binom.sf(n_plus - 1, n_eff, 0.5)
        if pval < alpha:
            rejections += 1
            
    return float(rejections / n_sims), float(np.mean(c_a)), float(np.mean(c_amin))

def simulate_matched_rmst(n_pairs=10, mu_b_sec=12.0, mu_amin_sec=12.2, cv=0.20, rho=0.85, tau=30.0, margin=1.5, alpha=0.025, n_sims=5000):
    """Simulates statistical power for Restricted Mean Survival Time (RMST) non-inferiority under censoring."""
    np.random.seed(42 + n_pairs)
    mean_vec = [math.log(mu_b_sec), math.log(mu_amin_sec)]
    var_val = cv**2
    cov_val = rho * var_val
    cov_mat = [[var_val, cov_val], [cov_val, var_val]]
    
    samples = np.random.multivariate_normal(mean_vec, cov_mat, size=(n_sims, n_pairs))
    t_b = np.exp(samples[:, :, 0])
    t_amin = np.exp(samples[:, :, 1])
    
    x_b = np.minimum(t_b, tau)
    x_amin = np.minimum(t_amin, tau)
    
    diffs = x_amin - x_b
    means = np.mean(diffs, axis=1)
    stds = np.std(diffs, axis=1, ddof=1)
    se = stds / math.sqrt(n_pairs)
    
    df = n_pairs - 2 # adjusted for adaptive retention
    t_stat = (margin - means) / se
    t_crit = stats.t.ppf(1.0 - alpha, df)
    power = np.mean(t_stat > t_crit)
    
    return float(power), float(np.mean(t_b >= tau)), float(np.mean(t_amin >= tau)), float(np.mean(means)), float(np.mean(stds))

def simulate_renewal_drain(r_pool=4, t_hold_us=300.0, duty_cycle=0.85, n_sims=20000):
    """Simulates conditional renewal drain wait time accounting for active arrival readers."""
    np.random.seed(101 + r_pool)
    k_active = np.random.binomial(r_pool, duty_cycle, size=n_sims)
    
    waits = np.zeros(n_sims)
    for i in range(n_sims):
        k = k_active[i]
        if k == 0 or t_hold_us == 0.0:
            w_drain = 0.0
        else:
            u = np.random.uniform(0.0, t_hold_us, size=k)
            w_drain = np.max(u)
        waits[i] = w_drain
        
    return {
        'r_pool': r_pool,
        't_hold_us': t_hold_us,
        'duty_cycle': duty_cycle,
        'mean_wait_us': float(np.mean(waits)),
        'std_wait_us': float(np.std(waits)),
        'median_wait_us': float(np.median(waits)),
        'theoretical_drain_slope': float(r_pool / (r_pool + 1.0)),
        'empirical_effective_slope': float(np.mean(waits) / t_hold_us) if t_hold_us > 0 else 0.0
    }

def main():
    print("=" * 80)
    print("PHASE I7 CHECKPOINT R1.2e: CORRECTED STATISTICAL SIMULATION SUITE")
    print("=" * 80)

    margin_10 = math.log(0.90) # -0.10536
    sample_sizes = [4, 6, 8, 10]

    workload_regimes = {
        'regime_1_closed_loop_low_variance': {'label': 'Closed-loop writer, low reader contention (r<=4)', 'sigma': 0.06},
        'regime_2_paced_moderate_variance': {'label': 'Paced writer (250 w/s), moderate contention (r=4)', 'sigma': 0.12},
        'regime_3_heavy_contention_high_variance': {'label': 'Heavy reader contention (r=8), scheduler active', 'sigma': 0.18},
        'regime_4_oversaturated_preemption': {'label': 'Oversaturated reader load (r=16), high preemption', 'sigma': 0.25}
    }

    out = {
        'metadata': {
            'protocol': 'Phase I7 Checkpoint R1.2e Bounded Protocol Corrections Simulation',
            'evaluated_git_revision': 'f4cce4b5faa80924c7ef945139ef1a65ccb2b32a',
            'alpha_efficacy_family': 0.025,
            'alpha_causal_family': 0.025,
            'margin_non_inferiority_10pct': margin_10,
            'mes_writer_superiority_w_per_s': 5.0,
            'rmst_non_inferiority_margin_seconds': 1.5,
            'cf_benefit_gate_threshold_w_per_s': 5.0,
            'cf_target_claim_threshold': 0.70,
            'empirical_mu_d_write': -0.01114,
            'prospective_sample_size_factor_empirical': 884.14,
            'global_execution_ceiling': 400,
            'max_confirmatory_n': 10,
            'min_confirmatory_n': 8
        },
        'workload_specific_sensitivity_matrix': {},
        'prospective_pilot_and_sample_size_rules': {},
        'causal_model_residual_reader_analysis': {},
        'capture_fraction_70pct_power': {},
        'exp4_censoring_and_survival_analysis': {}
    }

    print("\n--- 1. WORKLOAD-SPECIFIC SENSITIVITY AND PROSPECTIVE SIZING ---")
    for r_key, r_info in workload_regimes.items():
        s = r_info['sigma']
        out['workload_specific_sensitivity_matrix'][r_key] = {
            'description': r_info['label'],
            'sigma_d': s,
            'power_curves': {}
        }
        for th in [1.00, 0.9889, 0.9800]:
            mu = math.log(th)
            out['workload_specific_sensitivity_matrix'][r_key]['power_curves'][f'theta_{th:.4f}'] = {
                'alpha_0.025_df_adjusted_n2': {n: round(compute_t_power(n, mu, margin_10, s, 0.025, df_adj=2), 4) for n in sample_sizes},
                'alpha_0.025_df_unadjusted_n1': {n: round(compute_t_power(n, mu, margin_10, s, 0.025, df_adj=1), 4) for n in sample_sizes}
            }
        
        n_req, factor = compute_required_n(s, margin_10, -0.01114, 0.025, 0.80)
        n_confirmatory = min(10, max(8, n_req))
        
        out['prospective_pilot_and_sample_size_rules'][r_key] = {
            'sigma_d': s,
            'pilot_sample_size': 4,
            'n_req_empirical_write_factor_884': n_req,
            'prospective_confirmatory_n_bounded': n_confirmatory,
            'realized_power_at_n_confirmatory_df_adj2': round(compute_t_power(n_confirmatory, -0.01114, margin_10, s, 0.025, df_adj=2), 4),
            'epistemic_classification_if_not_rejected': 'Conclusive Non-Inferiority' if n_confirmatory >= n_req else 'Inconclusive Evidence (Power Reduced by 400-Ceiling)'
        }
        print(f"[{r_key}] sigma={s:.2f} -> N_req={n_req:2d} | Bounded N={n_confirmatory:2d} (Realized Power={out['prospective_pilot_and_sample_size_rules'][r_key]['realized_power_at_n_confirmatory_df_adj2']*100:.1f}%) | Classification: {out['prospective_pilot_and_sample_size_rules'][r_key]['epistemic_classification_if_not_rejected']}")

    print("\n--- 2. EXP-2 CAUSAL MODEL: CONTROLS & SEPARATION ---")
    causal_sims = [
        ('c2_01_baseline_zero_delay', 4, 0.0, 0.95),
        ('c2_02_renewal_point_1', 4, 100.0, 0.95),
        ('c2_03_renewal_point_2', 4, 300.0, 0.95),
        ('c2_04_renewal_point_3', 4, 500.0, 0.95),
        ('c2_05_concurrency_scaling', 2, 300.0, 0.95),
        ('c2_06_cpu_load_matched_negative_control', 4, 0.0, 0.0) # 300 us spin OUTSIDE lock
    ]
    for sim_key, r_p, t_h, duty in causal_sims:
        sim_res = simulate_renewal_drain(r_p, t_h, duty)
        out['causal_model_residual_reader_analysis'][sim_key] = sim_res
        print(f"[{sim_key}] Mean wait={sim_res['mean_wait_us']:.1f} us (SD={sim_res['std_wait_us']:.1f} us) | Slope={sim_res['empirical_effective_slope']:.3f} vs Theo={sim_res['theoretical_drain_slope']:.3f}")

    print("\n--- 3. CAPTURE FRACTION (CF >= 0.70) STATISTICAL POWER ---")
    for cf in [0.75, 0.80, 0.85]:
        out['capture_fraction_70pct_power'][f'cf_true_{cf:.2f}'] = {}
        for n in [6, 8, 10]:
            pwr, se = cf_power_delta(n, cf)
            out['capture_fraction_70pct_power'][f'cf_true_{cf:.2f}'][f'N_{n}'] = {
                'power_alpha_025': round(pwr, 4),
                'se_cf': round(se, 4)
            }
        print(f"CF_true={cf:.2f}: Power at N=8 -> {out['capture_fraction_70pct_power'][f'cf_true_{cf:.2f}']['N_8']['power_alpha_025']*100:.1f}%, N=10 -> {out['capture_fraction_70pct_power'][f'cf_true_{cf:.2f}']['N_10']['power_alpha_025']*100:.1f}%")

    print("\n--- 4. EXP-4 CENSORED ANALYSIS: DIRECTIONAL SIGN TEST & RMST ---")
    sign_regimes = [
        ('severe_starvation', math.log(28.0), 0.35, math.log(9.5), 0.15),
        ('medium_contention', math.log(18.0), 0.40, math.log(9.5), 0.15),
        ('modest_difference', math.log(13.0), 0.30, math.log(9.5), 0.15)
    ]
    out['exp4_censoring_and_survival_analysis']['h4_1_paired_sign_test'] = {}
    for r_name, m_a, s_a, m_am, s_am in sign_regimes:
        out['exp4_censoring_and_survival_analysis']['h4_1_paired_sign_test'][r_name] = {}
        for n in [8, 10]:
            pwr, c_a, c_am = simulate_exp4_sign_test(n, m_a, s_a, m_am, s_am, alpha=0.025)
            out['exp4_censoring_and_survival_analysis']['h4_1_paired_sign_test'][r_name][f'N_{n}'] = {
                'power_alpha_025': round(pwr, 4),
                'timeout_rate_a': round(c_a, 4),
                'timeout_rate_amin': round(c_am, 4)
            }
        print(f"[Sign Test: {r_name}] N=10 Power(alpha=0.025) = {out['exp4_censoring_and_survival_analysis']['h4_1_paired_sign_test'][r_name]['N_10']['power_alpha_025']*100:.1f}% (A timeout: {c_a*100:.1f}%)")

    rmst_scenarios = [
        ('low_contention_fast_workload', 10.0, 10.2, 0.15, 0.85, 1.5),
        ('moderate_contention_workload', 15.0, 15.3, 0.20, 0.85, 1.5),
        ('heavy_contention_censored_workload', 22.0, 22.5, 0.25, 0.85, 2.5)
    ]
    out['exp4_censoring_and_survival_analysis']['h4_2_rmst_non_inferiority'] = {}
    for sc_name, mb, mam, cv, rho, marg in rmst_scenarios:
        out['exp4_censoring_and_survival_analysis']['h4_2_rmst_non_inferiority'][sc_name] = {'margin_sec': marg, 'results': {}}
        for n in [8, 10]:
            pwr, cb, cam, md, sd = simulate_matched_rmst(n, mb, mam, cv, rho, tau=30.0, margin=marg, alpha=0.025)
            out['exp4_censoring_and_survival_analysis']['h4_2_rmst_non_inferiority'][sc_name]['results'][f'N_{n}'] = {
                'power_alpha_025': round(pwr, 4),
                'censoring_b': round(cb, 4),
                'censoring_amin': round(cam, 4),
                'mean_paired_diff_sec': round(md, 4),
                'sd_paired_diff_sec': round(sd, 4)
            }
        print(f"[RMST NI: {sc_name}] N=10 Power = {out['exp4_censoring_and_survival_analysis']['h4_2_rmst_non_inferiority'][sc_name]['results']['N_10']['power_alpha_025']*100:.1f}% (Margin: {marg}s, Diff: {md:.2f}s, CensB: {cb*100:.1f}%)")

    out_path = os.path.join(os.path.dirname(__file__), "r1_2e_power_simulation_results.json")
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=2)

    print(f"\n[OK] Corrected simulation results successfully exported to: {out_path}")

if __name__ == "__main__":
    main()
