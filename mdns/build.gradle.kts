plugins { `java-library` }
java {
    sourceCompatibility = JavaVersion.VERSION_1_8
    targetCompatibility = JavaVersion.VERSION_1_8
}
dependencies {
    api("org.slf4j:slf4j-api:2.0.17")
    testImplementation("junit:junit:4.13.2")
}
