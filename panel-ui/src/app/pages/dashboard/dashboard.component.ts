import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor, NgClass } from '@angular/common';
import { ApiService } from '../../services/api.service';
import { AuthService } from '../../services/auth.service';
import { HealthStatus, ComponentStatus, PodStatus } from '../../models';

@Component({
  selector: 'app-dashboard',
  standalone: true,
  imports: [NgIf, NgFor, NgClass],
  template: `
    <h2>Dashboard</h2>

    <div *ngIf="error" class="alert error">{{ error }}</div>

    <!-- Admin: Platform Health -->
    <div *ngIf="auth.isAdmin && health">
      <h3>Platform Health</h3>

      <div class="grid">
        <div class="card">
          <h4>Deployments</h4>
          <table>
            <thead><tr><th>Name</th><th>Ready</th><th>Status</th></tr></thead>
            <tbody>
              <tr *ngFor="let d of health.deployments">
                <td>{{ d.name }}</td>
                <td>{{ d.ready }}/{{ d.desired }}</td>
                <td><span class="badge" [ngClass]="d.available ? 'green' : 'red'">
                  {{ d.available ? 'Available' : 'Unavailable' }}
                </span></td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="card">
          <h4>StatefulSets</h4>
          <table>
            <thead><tr><th>Name</th><th>Ready</th><th>Status</th></tr></thead>
            <tbody>
              <tr *ngFor="let s of health.statefulSets">
                <td>{{ s.name }}</td>
                <td>{{ s.ready }}/{{ s.desired }}</td>
                <td><span class="badge" [ngClass]="s.available ? 'green' : 'red'">
                  {{ s.available ? 'Available' : 'Unavailable' }}
                </span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Pod alerts -->
      <div *ngIf="alertPods.length > 0" class="card">
        <h4>Pod Alerts</h4>
        <div *ngFor="let p of alertPods" class="alert warning">
          <strong>{{ p.name }}</strong> — Phase: {{ p.phase }}, Restarts: {{ p.restarts }}
          <span *ngFor="let c of p.containers">
            | {{ c.name }}: {{ c.state }} ({{ c.restartCount }} restarts)
          </span>
        </div>
      </div>

      <div class="card">
        <h4>Pods</h4>
        <table>
          <thead><tr><th>Name</th><th>Phase</th><th>Ready</th><th>Restarts</th></tr></thead>
          <tbody>
            <tr *ngFor="let p of health.pods">
              <td>{{ p.name }}</td>
              <td><span class="badge" [ngClass]="podBadge(p)">{{ p.phase }}</span></td>
              <td>{{ p.ready ? 'Yes' : 'No' }}</td>
              <td>{{ p.restarts }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- User: Quick overview -->
    <div *ngIf="!auth.isAdmin">
      <p>Welcome, {{ auth.username }}. Use the sidebar to manage your hosting resources.</p>
    </div>
  `,
  styles: [`
    .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
    h3 { margin-top: 0; }
    h4 { margin-top: 0; }
    @media (max-width: 900px) { .grid { grid-template-columns: 1fr; } }
  `]
})
export class DashboardComponent implements OnInit {
  health: HealthStatus | null = null;
  alertPods: PodStatus[] = [];
  error = '';

  constructor(public auth: AuthService, private api: ApiService) {}

  ngOnInit(): void {
    if (this.auth.isAdmin) {
      this.api.getHealth().subscribe({
        next: h => {
          this.health = h;
          this.alertPods = h.pods.filter(p =>
            p.phase === 'CrashLoopBackOff' || p.phase === 'Error' || p.phase === 'Failed' || p.restarts > 5
          );
        },
        error: () => this.error = 'Failed to load platform health status.'
      });
    }
  }

  podBadge(p: PodStatus): string {
    if (p.phase === 'Running' && p.ready) return 'green';
    if (p.phase === 'CrashLoopBackOff' || p.phase === 'Error' || p.phase === 'Failed') return 'red';
    return 'yellow';
  }
}
