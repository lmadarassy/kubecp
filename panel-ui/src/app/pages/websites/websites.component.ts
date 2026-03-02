import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor, NgClass } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';
import { Website } from '../../models';

@Component({
  selector: 'app-websites',
  standalone: true,
  imports: [NgIf, NgFor, NgClass, FormsModule],
  template: `
    <div class="page-header">
      <h2>Websites</h2>
      <button class="primary" (click)="showCreate = !showCreate">{{ showCreate ? 'Cancel' : 'Create Website' }}</button>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <!-- Create form -->
    <div *ngIf="showCreate" class="card">
      <h3>Create Website</h3>
      <div class="form-group">
        <label>Primary Domain</label>
        <input [(ngModel)]="createForm.primaryDomain" placeholder="example.com">
      </div>
      <div class="form-group">
        <label>PHP Version</label>
        <select [(ngModel)]="createForm.phpVersion">
          <option value="7.4">PHP 7.4</option>
          <option value="8.0">PHP 8.0</option>
          <option value="8.1">PHP 8.1</option>
          <option value="8.2">PHP 8.2</option>
          <option value="8.3">PHP 8.3</option>
          <option value="8.4">PHP 8.4</option>
          <option value="8.5">PHP 8.5</option>
        </select>
      </div>
      <div class="form-group">
        <label>SSL Mode</label>
        <select [(ngModel)]="createForm.sslMode">
          <option value="none">No SSL</option>
          <option value="letsencrypt">Let's Encrypt (automatic ACME)</option>
          <option value="selfsigned">Self-signed (auto-generated)</option>
          <option value="custom">Custom certificate (upload)</option>
        </select>
      </div>
      <div *ngIf="createForm.sslMode === 'custom'" class="form-group">
        <label>CA Certificate (PEM) — optional, for certificate chain</label>
        <textarea [(ngModel)]="createForm.caCert" rows="4" placeholder="-----BEGIN CERTIFICATE-----&#10;...CA cert...&#10;-----END CERTIFICATE-----"></textarea>
      </div>
      <div *ngIf="createForm.sslMode === 'custom'" class="form-group">
        <label>Server Certificate (PEM) — your domain's certificate</label>
        <textarea [(ngModel)]="createForm.tlsCert" rows="4" placeholder="-----BEGIN CERTIFICATE-----&#10;...server cert...&#10;-----END CERTIFICATE-----"></textarea>
      </div>
      <div *ngIf="createForm.sslMode === 'custom'" class="form-group">
        <label>Private Key (PEM) — matching the server certificate</label>
        <textarea [(ngModel)]="createForm.tlsKey" rows="4" placeholder="-----BEGIN PRIVATE KEY-----&#10;...key...&#10;-----END PRIVATE KEY-----"></textarea>
      </div>
      <button class="primary" (click)="create()">Create</button>
    </div>

    <!-- Add alias form -->
    <div *ngIf="addingDomainTo" class="card">
      <h3>Add Alias to {{ addingDomainTo.primaryDomain }}</h3>
      <div class="form-group">
        <label>Domain</label>
        <input [(ngModel)]="domainForm.name" placeholder="www.example.com">
      </div>
      <div class="form-group">
        <label>SSL Mode</label>
        <select [(ngModel)]="domainForm.sslMode">
          <option value="none">No SSL</option>
          <option value="letsencrypt">Let's Encrypt (automatic ACME)</option>
          <option value="selfsigned">Self-signed (auto-generated)</option>
          <option value="custom">Custom certificate (upload)</option>
        </select>
      </div>
      <div *ngIf="domainForm.sslMode === 'custom'" class="form-group">
        <label>CA Certificate (PEM) — optional</label>
        <textarea [(ngModel)]="domainForm.caCert" rows="3" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
      </div>
      <div *ngIf="domainForm.sslMode === 'custom'" class="form-group">
        <label>Server Certificate (PEM)</label>
        <textarea [(ngModel)]="domainForm.tlsCert" rows="3" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
      </div>
      <div *ngIf="domainForm.sslMode === 'custom'" class="form-group">
        <label>Private Key (PEM)</label>
        <textarea [(ngModel)]="domainForm.tlsKey" rows="3" placeholder="-----BEGIN PRIVATE KEY-----"></textarea>
      </div>
      <button class="primary" (click)="addDomain()">Add Domain</button>
      <button (click)="addingDomainTo = null">Cancel</button>
    </div>

    <!-- Website list -->
    <div *ngFor="let w of websites" class="card">
      <div class="page-header">
        <div>
          <strong>{{ w.primaryDomain }}</strong>
          <span class="badge" [ngClass]="phaseBadge(w.status.phase)">{{ w.status.phase }}</span>
          <span style="margin-left:8px;color:#6b7280">PHP {{ w.php.version }}</span>
          <span style="margin-left:8px;color:#6b7280">{{ w.status.readyReplicas }}/{{ w.status.replicas }} replicas</span>
        </div>
        <div>
          <button (click)="addingDomainTo = w; domainForm = { name: '', sslMode: 'letsencrypt', caCert: '', tlsCert: '', tlsKey: '' }">Add Alias</button>
          <button class="danger" (click)="remove(w)">Delete</button>
        </div>
      </div>
      <table *ngIf="w.status.domains && w.status.domains.length">
        <thead><tr><th>Domain</th><th>SSL Mode</th><th>Certificate</th><th>Expiry</th><th>Actions</th></tr></thead>
        <tbody>
          <tr *ngFor="let d of w.status.domains">
            <td>{{ d.name }}</td>
            <td>{{ w.ssl?.mode || 'none' }}</td>
            <td><span class="badge" [ngClass]="certBadge(d.certificateStatus)">{{ d.certificateStatus }}</span></td>
            <td>{{ d.certificateExpiry || '—' }}</td>
            <td><button *ngIf="d.name !== w.primaryDomain" class="danger" (click)="removeDomain(w, d.name)">Remove</button></td>
          </tr>
        </tbody>
      </table>
    </div>
  `
})
export class WebsitesComponent implements OnInit {
  websites: Website[] = [];
  showCreate = false;
  createForm = { primaryDomain: '', phpVersion: '8.2', sslMode: 'letsencrypt', caCert: '', tlsCert: '', tlsKey: '' };
  addingDomainTo: Website | null = null;
  domainForm = { name: '', sslMode: 'letsencrypt', caCert: '', tlsCert: '', tlsKey: '' };
  error = '';
  success = '';

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.api.getWebsites().subscribe({
      next: w => this.websites = w,
      error: () => this.error = 'Failed to load websites.'
    });
  }

  getDomainSSLMode(w: Website, domainName: string): string {
    if (!w.ssl?.enabled) return 'none';
    return w.ssl.mode || 'letsencrypt';
  }

  create(): void {
    this.error = ''; this.success = '';
    if (!this.createForm.primaryDomain) {
      this.error = 'Primary domain is required.';
      return;
    }
    const sslEnabled = this.createForm.sslMode !== 'none';
    const data: any = {
      primaryDomain: this.createForm.primaryDomain,
      php: { version: this.createForm.phpVersion },
      ssl: {
        enabled: sslEnabled,
        mode: this.createForm.sslMode,
        issuer: this.createForm.sslMode === 'letsencrypt' ? 'letsencrypt-production' : ''
      }
    };

    // For custom mode, upload cert first then create website
    if (this.createForm.sslMode === 'custom') {
      if (!this.createForm.tlsCert || !this.createForm.tlsKey) {
        this.error = 'Server certificate and private key are required for custom SSL.';
        return;
      }
      this.api.uploadCertificate({
        domain: this.createForm.primaryDomain,
        tlsCert: this.createForm.tlsCert,
        tlsKey: this.createForm.tlsKey,
        caCert: this.createForm.caCert || undefined
      }).subscribe({
        next: (resp: any) => {
          data.ssl.secretName = resp.name;
          this.doCreateWebsite(data);
        },
        error: (e: any) => this.error = e.error?.error?.message || 'Certificate upload failed.'
      });
      return;
    }

    this.doCreateWebsite(data);
  }

  private doCreateWebsite(data: any): void {
    this.api.createWebsite(data).subscribe({
      next: () => { this.success = 'Website created.'; this.showCreate = false; this.createForm = { primaryDomain: '', phpVersion: '8.2', sslMode: 'letsencrypt', caCert: '', tlsCert: '', tlsKey: '' }; this.load(); },
      error: (e: any) => this.error = e.error?.error?.message || 'Creation failed.'
    });
  }

  addDomain(): void {
    if (!this.addingDomainTo) return;
    this.error = ''; this.success = '';
    if (!this.domainForm.name) {
      this.error = 'Domain name is required.';
      return;
    }
    this.api.addAlias(this.addingDomainTo!.id, this.domainForm.name).subscribe({
      next: () => { this.success = 'Alias added.'; this.addingDomainTo = null; this.load(); },
      error: (e: any) => this.error = e.error?.error?.message || 'Failed to add alias.'
    });
  }

  private doAddDomain(domainData: any): void {
    this.api.addDomain(this.addingDomainTo!.id, domainData).subscribe({
      next: () => { this.success = 'Domain added.'; this.addingDomainTo = null; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Failed to add domain.'
    });
  }

  removeDomain(w: Website, domain: string): void {
    if (!confirm(`Remove alias "${domain}"?`)) return;
    this.api.removeAlias(w.id, domain).subscribe({
      next: () => { this.success = 'Alias removed.'; this.load(); },
      error: (e: any) => this.error = e.error?.error?.message || 'Failed to remove alias.'
    });
  }

  remove(w: Website): void {
    if (!confirm(`Delete website "${w.name}"?`)) return;
    this.api.deleteWebsite(w.id).subscribe({
      next: () => { this.success = 'Website deleted.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Delete failed.'
    });
  }

  phaseBadge(phase: string): string {
    if (phase === 'Running') return 'green';
    if (phase === 'Error' || phase === 'Terminating') return 'red';
    return 'yellow';
  }

  certBadge(status: string): string {
    if (status === 'Valid') return 'green';
    if (status === 'Expiring') return 'yellow';
    if (status === 'Expired' || status === 'Error') return 'red';
    return 'blue';
  }
}
