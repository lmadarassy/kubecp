import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor, NgClass } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';
import { Certificate } from '../../models';

@Component({
  selector: 'app-certificates',
  standalone: true,
  imports: [NgIf, NgFor, NgClass, FormsModule],
  template: `
    <div class="page-header">
      <h2>Certificates</h2>
      <div>
        <button class="primary" (click)="showUpload = !showUpload; showSelfSigned = false">
          {{ showUpload ? 'Cancel' : 'Upload Certificate' }}
        </button>
        <button style="margin-left:8px" (click)="showSelfSigned = !showSelfSigned; showUpload = false">
          {{ showSelfSigned ? 'Cancel' : 'Generate Self-Signed' }}
        </button>
      </div>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <!-- Upload custom certificate form -->
    <div *ngIf="showUpload" class="card">
      <h3>Upload Custom Certificate</h3>
      <p style="color:#6b7280;font-size:0.9em">Upload your own TLS certificate for a domain. The server certificate and private key are required. The CA certificate is optional but recommended for full chain validation.</p>
      <div class="form-group">
        <label>Domain</label>
        <input [(ngModel)]="uploadForm.domain" placeholder="example.com">
      </div>
      <div class="form-group">
        <label>CA Certificate (PEM) — optional, intermediate/root CA for chain</label>
        <textarea [(ngModel)]="uploadForm.caCert" rows="4" placeholder="-----BEGIN CERTIFICATE-----&#10;...CA certificate...&#10;-----END CERTIFICATE-----"></textarea>
      </div>
      <div class="form-group">
        <label>Server Certificate (PEM) — your domain's TLS certificate</label>
        <textarea [(ngModel)]="uploadForm.tlsCert" rows="4" placeholder="-----BEGIN CERTIFICATE-----&#10;...server certificate...&#10;-----END CERTIFICATE-----"></textarea>
      </div>
      <div class="form-group">
        <label>Private Key (PEM) — matching the server certificate above</label>
        <textarea [(ngModel)]="uploadForm.tlsKey" rows="4" placeholder="-----BEGIN PRIVATE KEY-----&#10;...private key...&#10;-----END PRIVATE KEY-----"></textarea>
      </div>
      <button class="primary" (click)="upload()">Upload</button>
    </div>

    <!-- Generate self-signed form -->
    <div *ngIf="showSelfSigned" class="card">
      <h3>Generate Self-Signed Certificate</h3>
      <p style="color:#6b7280;font-size:0.9em">Auto-generate a self-signed TLS certificate for a domain. Useful for development or internal use. Browsers will show a warning for self-signed certificates.</p>
      <div class="form-group">
        <label>Domain</label>
        <input [(ngModel)]="selfSignedDomain" placeholder="example.com">
      </div>
      <button class="primary" (click)="generateSelfSigned()">Generate</button>
    </div>

    <!-- Edit certificate form -->
    <div *ngIf="editingCert" class="card">
      <h3>Update Certificate: {{ editingCert.name }}</h3>
      <p style="color:#6b7280;font-size:0.9em">Re-upload the certificate files to update this certificate.</p>
      <div class="form-group">
        <label>CA Certificate (PEM) — optional</label>
        <textarea [(ngModel)]="editForm.caCert" rows="3" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
      </div>
      <div class="form-group">
        <label>Server Certificate (PEM)</label>
        <textarea [(ngModel)]="editForm.tlsCert" rows="3" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
      </div>
      <div class="form-group">
        <label>Private Key (PEM)</label>
        <textarea [(ngModel)]="editForm.tlsKey" rows="3" placeholder="-----BEGIN PRIVATE KEY-----"></textarea>
      </div>
      <button class="primary" (click)="updateCert()">Update</button>
      <button (click)="editingCert = null">Cancel</button>
    </div>

    <!-- Certificate list -->
    <table>
      <thead>
        <tr><th>Name</th><th>Domain</th><th>Mode</th><th>Issuer</th><th>Status</th><th>Expiry</th><th>Actions</th></tr>
      </thead>
      <tbody>
        <tr *ngFor="let c of certificates">
          <td>{{ c.name }}</td>
          <td>{{ c.domain }}</td>
          <td><span class="badge blue">{{ c.mode }}</span></td>
          <td>{{ c.issuer }}</td>
          <td><span class="badge" [ngClass]="statusBadge(c.status)">{{ c.status }}</span></td>
          <td>{{ c.expiry || '—' }}</td>
          <td>
            <button *ngIf="c.mode === 'custom'" (click)="startEdit(c)">Edit</button>
            <button class="danger" (click)="deleteCert(c)">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>
  `
})
export class CertificatesComponent implements OnInit {
  certificates: Certificate[] = [];
  showUpload = false;
  showSelfSigned = false;
  uploadForm = { domain: '', caCert: '', tlsCert: '', tlsKey: '' };
  selfSignedDomain = '';
  editingCert: Certificate | null = null;
  editForm = { caCert: '', tlsCert: '', tlsKey: '' };
  error = '';
  success = '';

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.api.getCertificates().subscribe({
      next: c => this.certificates = c,
      error: () => this.error = 'Failed to load certificates.'
    });
  }

  upload(): void {
    if (!this.uploadForm.domain) { this.error = 'Domain is required.'; return; }
    if (!this.uploadForm.tlsCert || !this.uploadForm.tlsKey) {
      this.error = 'Server certificate and private key are required.'; return;
    }
    this.error = ''; this.success = '';
    this.api.uploadCertificate({
      domain: this.uploadForm.domain,
      tlsCert: this.uploadForm.tlsCert,
      tlsKey: this.uploadForm.tlsKey,
      caCert: this.uploadForm.caCert || undefined
    }).subscribe({
      next: () => {
        this.success = 'Certificate uploaded successfully.';
        this.showUpload = false;
        this.uploadForm = { domain: '', caCert: '', tlsCert: '', tlsKey: '' };
        this.load();
      },
      error: (e) => this.error = e.error?.error?.message || 'Upload failed.'
    });
  }

  generateSelfSigned(): void {
    if (!this.selfSignedDomain) { this.error = 'Domain is required.'; return; }
    this.error = ''; this.success = '';
    this.api.generateSelfSigned(this.selfSignedDomain).subscribe({
      next: () => {
        this.success = 'Self-signed certificate generated.';
        this.showSelfSigned = false;
        this.selfSignedDomain = '';
        this.load();
      },
      error: (e) => this.error = e.error?.error?.message || 'Generation failed.'
    });
  }

  startEdit(cert: Certificate): void {
    this.editingCert = cert;
    this.editForm = { caCert: '', tlsCert: '', tlsKey: '' };
  }

  updateCert(): void {
    if (!this.editingCert) return;
    if (!this.editForm.tlsCert || !this.editForm.tlsKey) {
      this.error = 'Server certificate and private key are required.'; return;
    }
    this.error = ''; this.success = '';
    this.api.updateCertificate(this.editingCert.name, {
      tlsCert: this.editForm.tlsCert,
      tlsKey: this.editForm.tlsKey,
      caCert: this.editForm.caCert || undefined
    }).subscribe({
      next: () => {
        this.success = 'Certificate updated.';
        this.editingCert = null;
        this.load();
      },
      error: (e) => this.error = e.error?.error?.message || 'Update failed.'
    });
  }

  deleteCert(cert: Certificate): void {
    if (!confirm(`Delete certificate "${cert.name}" for ${cert.domain}?`)) return;
    this.api.deleteCertificate(cert.name).subscribe({
      next: () => { this.success = 'Certificate deleted.'; this.load(); },
      error: (e) => this.error = e.error?.error?.message || 'Delete failed.'
    });
  }

  statusBadge(status: string): string {
    if (status === 'Valid') return 'green';
    if (status === 'Expiring') return 'yellow';
    if (status === 'Expired' || status === 'Error') return 'red';
    return 'blue';
  }
}
