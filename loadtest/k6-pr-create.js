import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
    vus: 10,
    duration: '30s'
};

const BASE_URL = 'http://localhost:8080';
const ADMIN_TOKEN = 'supersecretadmin';

export default function () {
    const prId = `pr-${__VU}-${__ITER}`;

    const payload = JSON.stringify({
        pull_request_id: prId,
        pull_request_name: 'Load test PR',
        author_id: 'u1'
    });

    const params = {
        headers: {
            'Content-Type': 'application/json',
            'X-Admin-Token': ADMIN_TOKEN
        }
    };

    const res = http.post(`${BASE_URL}/pullRequest/create`, payload, params);

    check(res, {
        'status is 201 or 409': (r) => r.status === 201 || r.status === 409
    });

    sleep(0.1);
}