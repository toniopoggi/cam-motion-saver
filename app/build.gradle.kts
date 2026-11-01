plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}
android {
    namespace = "org.camfreenest.android"
    compileSdk = 35
    defaultConfig {
        applicationId = "org.camfreenest.android.preview"
        minSdk = 28
        targetSdk = 28
        versionCode = 64
        versionName = "0.5.14-preview"
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}
dependencies {
    implementation(files("libs/engine.aar"))
    implementation(project(":mdns"))
    implementation("org.slf4j:slf4j-nop:2.0.17")
}
